package consumers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/bengobox/logistics-service/internal/modules/tasks"
	"github.com/bengobox/logistics-service/internal/platform/marketflow"
)

// memStep is a task step's type and contact.
type memStep struct{ Type, Name, Phone string }

// memTask is one task: its status, metadata and steps.
type memTask struct {
	Status string
	Meta   map[string]any
	Steps  []memStep
}

// memTasks is taskMetadataStore over in-memory tasks, matching crm_contact_id the way
// tasks.MetadataContact does (lower-cased text against the canonical id) and clearing drop-off
// contacts on finished tasks only, as entTaskStore does.
type memTasks struct {
	tasks []*memTask
	err   error
}

func (m *memTasks) matches(meta map[string]any, contactID uuid.UUID) bool {
	s, _ := meta["crm_contact_id"].(string)
	return strings.ToLower(s) == contactID.String()
}

func (st memStep) dropoffContact() bool {
	return st.Type == "dropoff" && (st.Name != "" || st.Phone != "")
}

func (m *memTasks) ContactTasks(_ context.Context, contactID uuid.UUID) ([]contactTask, error) {
	if m.err != nil {
		return nil, m.err
	}
	var out []contactTask
	for _, t := range m.tasks {
		if !m.matches(t.Meta, contactID) {
			continue
		}
		ct := contactTask{Status: t.Status, Metadata: t.Meta}
		for _, st := range t.Steps {
			ct.DropoffContact = ct.DropoffContact || st.dropoffContact()
		}
		out = append(out, ct)
	}
	return out, nil
}

func (m *memTasks) RewriteContact(_ context.Context, contactID uuid.UUID, edit func(map[string]any) bool, clearFinishedSteps bool) (rewriteResult, error) {
	if m.err != nil {
		return rewriteResult{}, m.err
	}
	var res rewriteResult
	for _, t := range m.tasks {
		if !m.matches(t.Meta, contactID) {
			continue
		}
		if clearFinishedSteps && tasks.IsTerminal(t.Status) {
			for i, st := range t.Steps {
				if st.dropoffContact() {
					t.Steps[i].Name, t.Steps[i].Phone = "", ""
					res.Steps++
				}
			}
		}
		cp := make(map[string]any, len(t.Meta))
		for k, v := range t.Meta {
			cp[k] = v
		}
		if edit(cp) {
			t.Meta = cp
			res.Tasks++
		}
	}
	return res, nil
}

// fakeCRM answers GetContact from a map and counts calls.
type fakeCRM struct {
	contacts map[uuid.UUID]marketflow.Contact
	err      error
	calls    int
	tenant   uuid.UUID
}

func (f *fakeCRM) GetContact(_ context.Context, tenantID, contactID uuid.UUID) (marketflow.Contact, error) {
	f.calls++
	f.tenant = tenantID
	if f.err != nil {
		return marketflow.Contact{}, f.err
	}
	c, ok := f.contacts[contactID]
	if !ok {
		return marketflow.Contact{}, marketflow.ErrContactNotFound
	}
	return c, nil
}

// customerTask is a task in status with the customer copies in its metadata and on its drop-off
// step (the pickup step holds the outlet's contact).
func customerTask(contact, status string) *memTask {
	return &memTask{
		Status: status,
		Meta: map[string]any{
			"crm_contact_id": contact, "customer_name": "Amina Otieno", "customer_phone": "0712000111",
			"customer_email": "amina@example.com", "order_number": "POS-1",
		},
		Steps: []memStep{
			{Type: "pickup", Name: "Main outlet", Phone: "0700000000"},
			{Type: "dropoff", Name: "Amina Otieno", Phone: "0712000111"},
		},
	}
}

func dropoff(t *memTask) memStep { return t.Steps[1] }

// A deleted CRM contact is unlinked from every task that names it, in any tenant and whatever the
// letter case of the stored id: the id and the name, phone and email copies go, the rest of the
// metadata stays, other contacts' tasks are untouched, and a second delivery changes nothing.
func TestCRMContactDeletedClearsTasks(t *testing.T) {
	gone, other := uuid.New(), uuid.New()
	store := &memTasks{tasks: []*memTask{
		customerTask(gone.String(), "completed"),
		customerTask(strings.ToUpper(gone.String()), "delivered"),
		customerTask(other.String(), "completed"),
		{Status: "pending", Meta: map[string]any{"order_number": "POS-9"}},
	}}
	crm := &fakeCRM{}
	c := &CRMContactConsumer{store: store, crm: crm, log: zap.NewNop()}

	res, err := c.ContactDeleted(context.Background(), gone)
	if err != nil || res.Tasks != 2 {
		t.Fatalf("ContactDeleted = %+v, %v; want 2 tasks", res, err)
	}
	for _, task := range store.tasks[:2] {
		for _, k := range []string{"crm_contact_id", "customer_name", "customer_phone", "customer_email"} {
			if _, ok := task.Meta[k]; ok {
				t.Fatalf("%s kept after delete: %v", k, task.Meta)
			}
		}
		if task.Meta["order_number"] != "POS-1" {
			t.Fatalf("the rest of the metadata must stay: %v", task.Meta)
		}
	}
	if store.tasks[2].Meta["customer_name"] != "Amina Otieno" || store.tasks[2].Meta["crm_contact_id"] != other.String() ||
		dropoff(store.tasks[2]).Phone != "0712000111" {
		t.Fatalf("another contact's task changed: %+v", store.tasks[2])
	}
	if crm.calls != 0 {
		t.Fatalf("a delete needs no CRM call, made %d", crm.calls)
	}
	if res, err := c.ContactDeleted(context.Background(), gone); err != nil || res != (rewriteResult{}) {
		t.Fatalf("repeat delivery = %+v, %v; want nothing changed", res, err)
	}
}

// On a delete the drop-off contact (name and phone) is cleared on finished tasks only: delivered,
// completed, cancelled, failed and returned. An active task keeps it so the rider can still call
// the customer mid-delivery, and the pickup step (the outlet's contact) is never touched.
func TestCRMContactDeletedClearsFinishedDropoffsOnly(t *testing.T) {
	id := uuid.New()
	finished := []string{"delivered", "completed", "cancelled", "failed", "returned"}
	active := []string{"pending", "assigned", "picked_up", "en_route_dropoff"}
	store := &memTasks{}
	for _, s := range append(append([]string{}, finished...), active...) {
		store.tasks = append(store.tasks, customerTask(id.String(), s))
	}
	c := &CRMContactConsumer{store: store, crm: &fakeCRM{}, log: zap.NewNop()}

	res, err := c.ContactDeleted(context.Background(), id)
	if err != nil || res.Steps != len(finished) || res.Tasks != len(finished)+len(active) {
		t.Fatalf("ContactDeleted = %+v, %v; want %d steps cleared on %d tasks", res, err, len(finished), len(finished)+len(active))
	}
	for _, task := range store.tasks {
		d := dropoff(task)
		if tasks.IsTerminal(task.Status) && (d.Name != "" || d.Phone != "") {
			t.Fatalf("finished %s task kept its drop-off contact: %+v", task.Status, d)
		}
		if !tasks.IsTerminal(task.Status) && (d.Name != "Amina Otieno" || d.Phone != "0712000111") {
			t.Fatalf("active %s task lost its drop-off contact: %+v", task.Status, d)
		}
		if task.Steps[0].Name != "Main outlet" || task.Steps[0].Phone != "0700000000" {
			t.Fatalf("pickup step changed on a %s task: %+v", task.Status, task.Steps[0])
		}
		if _, ok := task.Meta["customer_phone"]; ok {
			t.Fatalf("metadata copies must go on every task, %s kept them", task.Status)
		}
	}
}

// An update that leaves the contact erased removes the copies, clears the finished tasks'
// drop-off contact and keeps the link, so the task still points at the contact. An active task
// keeps its drop-off contact. The CRM is asked in the event's tenant.
func TestCRMContactUpdatedErasedClearsCopies(t *testing.T) {
	id, tenant := uuid.New(), uuid.New()
	store := &memTasks{tasks: []*memTask{customerTask(id.String(), "completed"), customerTask(id.String(), "en_route_dropoff")}}
	crm := &fakeCRM{contacts: map[uuid.UUID]marketflow.Contact{id: {ID: id, FirstName: "Erased"}}}
	c := &CRMContactConsumer{store: store, crm: crm, log: zap.NewNop()}

	res, err := c.ContactUpdated(context.Background(), tenant, id)
	if err != nil || res.Tasks != 2 || res.Steps != 1 {
		t.Fatalf("ContactUpdated = %+v, %v; want 2 tasks and 1 step", res, err)
	}
	for _, task := range store.tasks {
		for _, k := range []string{"customer_name", "customer_phone", "customer_email"} {
			if _, ok := task.Meta[k]; ok {
				t.Fatalf("%s kept for an erased contact: %v", k, task.Meta)
			}
		}
		if task.Meta["crm_contact_id"] != id.String() || task.Meta["order_number"] != "POS-1" {
			t.Fatalf("the link and the rest must stay: %v", task.Meta)
		}
	}
	if d := dropoff(store.tasks[0]); d.Name != "" || d.Phone != "" {
		t.Fatalf("finished task kept its drop-off contact: %+v", d)
	}
	if d := dropoff(store.tasks[1]); d.Phone != "0712000111" {
		t.Fatalf("active task lost its drop-off contact: %+v", d)
	}
	if crm.tenant != tenant {
		t.Fatalf("CRM asked in tenant %s, want %s", crm.tenant, tenant)
	}
	// Repeat delivery: no copy left and the only drop-off contact is on an active task, so no CRM
	// call and no change.
	crm.calls = 0
	if res, err := c.ContactUpdated(context.Background(), tenant, id); err != nil || res != (rewriteResult{}) || crm.calls != 0 {
		t.Fatalf("repeat delivery = %+v, %v, %d CRM calls; want nothing", res, err, crm.calls)
	}
}

// A finished task with no metadata copies (made before logistics kept them) still has its drop-off
// contact cleared on an erasure.
func TestCRMContactUpdatedErasedClearsOldTaskDropoff(t *testing.T) {
	id := uuid.New()
	old := customerTask(id.String(), "delivered")
	old.Meta = map[string]any{"crm_contact_id": id.String()}
	store := &memTasks{tasks: []*memTask{old}}
	c := &CRMContactConsumer{store: store, crm: &fakeCRM{contacts: map[uuid.UUID]marketflow.Contact{id: {ID: id, FirstName: "Erased"}}}, log: zap.NewNop()}

	res, err := c.ContactUpdated(context.Background(), uuid.New(), id)
	if err != nil || res.Steps != 1 {
		t.Fatalf("ContactUpdated = %+v, %v; want 1 step cleared", res, err)
	}
	if d := dropoff(old); d.Name != "" || d.Phone != "" {
		t.Fatalf("drop-off contact kept: %+v", d)
	}
}

// A live contact's copies follow the CRM: each copy the task holds takes the CRM's value, a copy
// the CRM has no value for stays as the source order supplied it, a copy the task does not hold
// is not added, and step contacts are left alone.
func TestCRMContactUpdatedRefreshesCopies(t *testing.T) {
	id := uuid.New()
	full := customerTask(id.String(), "completed")
	noPhone := customerTask(id.String(), "completed")
	delete(noPhone.Meta, "customer_phone")
	store := &memTasks{tasks: []*memTask{full, noPhone}}
	crm := &fakeCRM{contacts: map[uuid.UUID]marketflow.Contact{
		id: {ID: id, FirstName: "Amina", LastName: "Wanjiru", Phone: "+254712999000"},
	}}
	c := &CRMContactConsumer{store: store, crm: crm, log: zap.NewNop()}

	res, err := c.ContactUpdated(context.Background(), uuid.New(), id)
	if err != nil || res.Tasks != 2 || res.Steps != 0 {
		t.Fatalf("ContactUpdated = %+v, %v; want 2 tasks, no steps", res, err)
	}
	if full.Meta["customer_name"] != "Amina Wanjiru" || full.Meta["customer_phone"] != "+254712999000" {
		t.Fatalf("copies not refreshed: %v", full.Meta)
	}
	if full.Meta["customer_email"] != "amina@example.com" {
		t.Fatalf("an email the CRM does not hold must stay: %v", full.Meta)
	}
	if _, added := noPhone.Meta["customer_phone"]; added {
		t.Fatalf("a copy the task did not hold was added: %v", noPhone.Meta)
	}
	if d := dropoff(full); d.Name != "Amina Otieno" || d.Phone != "0712000111" {
		t.Fatalf("a refresh must not touch step contacts: %+v", d)
	}
	// Nothing new in the CRM: no change on a repeat delivery.
	if res, err := c.ContactUpdated(context.Background(), uuid.New(), id); err != nil || res.Tasks != 0 {
		t.Fatalf("repeat delivery = %+v, %v; want 0 tasks", res, err)
	}
}

// Nothing to do: no task holds a copy or a finished drop-off contact (no CRM call), or the CRM no
// longer knows the contact.
func TestCRMContactUpdatedNoop(t *testing.T) {
	id := uuid.New()
	activeOnly := customerTask(id.String(), "picked_up")
	activeOnly.Meta = map[string]any{"crm_contact_id": id.String()}
	crm := &fakeCRM{}
	c := &CRMContactConsumer{store: &memTasks{tasks: []*memTask{activeOnly}}, crm: crm, log: zap.NewNop()}
	if res, err := c.ContactUpdated(context.Background(), uuid.New(), id); err != nil || res != (rewriteResult{}) || crm.calls != 0 {
		t.Fatalf("no copies: %+v, %v, %d CRM calls; want nothing", res, err, crm.calls)
	}

	task := customerTask(id.String(), "completed")
	c = &CRMContactConsumer{store: &memTasks{tasks: []*memTask{task}}, crm: crm, log: zap.NewNop()}
	if res, err := c.ContactUpdated(context.Background(), uuid.New(), id); err != nil || res != (rewriteResult{}) {
		t.Fatalf("unknown contact: %+v, %v; want nothing", res, err)
	}
	if task.Meta["customer_name"] != "Amina Otieno" || dropoff(task).Phone != "0712000111" {
		t.Fatalf("an unknown contact must change nothing: %+v", task)
	}
}

// When the CRM or the database cannot answer, the update is an error so the event is retried.
func TestCRMContactUpdatedFailuresRetry(t *testing.T) {
	id := uuid.New()
	c := &CRMContactConsumer{
		store: &memTasks{tasks: []*memTask{customerTask(id.String(), "completed")}},
		crm:   &fakeCRM{err: errors.New("marketflow down")}, log: zap.NewNop(),
	}
	if _, err := c.ContactUpdated(context.Background(), uuid.New(), id); err == nil {
		t.Fatal("CRM failure must be an error")
	}
	c = &CRMContactConsumer{store: &memTasks{err: errors.New("db down")}, crm: &fakeCRM{}, log: zap.NewNop()}
	if _, err := c.ContactUpdated(context.Background(), uuid.New(), id); err == nil {
		t.Fatal("database failure must be an error")
	}
	if _, err := c.ContactDeleted(context.Background(), id); err == nil {
		t.Fatal("database failure on delete must be an error")
	}
}

// Logs carry ids and counts only, never the person's details.
func TestCRMContactLogsIdsOnly(t *testing.T) {
	id := uuid.New()
	core, logs := observer.New(zapcore.DebugLevel)
	c := &CRMContactConsumer{
		store: &memTasks{tasks: []*memTask{customerTask(id.String(), "completed")}},
		crm:   &fakeCRM{contacts: map[uuid.UUID]marketflow.Contact{id: {ID: id, FirstName: "Erased"}}},
		log:   zap.New(core),
	}
	if _, err := c.ContactUpdated(context.Background(), uuid.New(), id); err != nil {
		t.Fatal(err)
	}
	if logs.Len() == 0 {
		t.Fatal("expected a log line")
	}
	for _, e := range logs.All() {
		line, _ := json.Marshal(e.ContextMap())
		for _, pii := range []string{"Amina", "0712000111", "amina@example.com"} {
			if strings.Contains(e.Message+string(line), pii) {
				t.Fatalf("log leaked %q: %s %s", pii, e.Message, line)
			}
		}
	}
}

type fakeMsg struct{ acked, naked bool }

func (m *fakeMsg) Ack(...nats.AckOpt) error                         { m.acked = true; return nil }
func (m *fakeMsg) NakWithDelay(time.Duration, ...nats.AckOpt) error { m.naked = true; return nil }

// An unreadable event is acked (a retry would not help); a failed apply is redelivered later.
func TestCRMContactSettle(t *testing.T) {
	c := &CRMContactConsumer{log: zap.NewNop()}
	ok := func(context.Context, *eventslib.Event) error { return nil }
	fail := func(context.Context, *eventslib.Event) error { return errors.New("db down") }
	evt, _ := json.Marshal(eventslib.Event{ID: uuid.New(), TenantID: uuid.New(), Payload: map[string]any{"contact_id": uuid.NewString()}})

	m := &fakeMsg{}
	c.settle(m, []byte("{not json"), fail)
	if !m.acked || m.naked {
		t.Fatalf("unreadable event: %+v, want acked", m)
	}
	m = &fakeMsg{}
	c.settle(m, evt, ok)
	if !m.acked || m.naked {
		t.Fatalf("applied event: %+v, want acked", m)
	}
	m = &fakeMsg{}
	c.settle(m, evt, fail)
	if m.acked || !m.naked {
		t.Fatalf("failed apply: %+v, want redelivered", m)
	}
}

// The contact id comes from the payload, else the aggregate id; neither gives uuid.Nil.
func TestCRMContactPayloadID(t *testing.T) {
	id, agg := uuid.New(), uuid.New()
	if got := payloadContactID(&eventslib.Event{Payload: map[string]any{"contact_id": " " + id.String() + " "}, AggregateID: agg}); got != id {
		t.Fatalf("payload id: %s", got)
	}
	if got := payloadContactID(&eventslib.Event{Payload: map[string]any{"contact_id": "x"}, AggregateID: agg}); got != agg {
		t.Fatalf("aggregate fallback: %s", got)
	}
	if got := payloadContactID(&eventslib.Event{}); got != uuid.Nil {
		t.Fatalf("no id: %s", got)
	}
}
