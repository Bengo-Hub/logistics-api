package consumers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/predicate"
	"github.com/bengobox/logistics-service/internal/ent/taskstep"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
	"github.com/bengobox/logistics-service/internal/platform/marketflow"
)

const (
	crmContactUpdatedSubject = "crm.contact.updated"
	crmContactDeletedSubject = "crm.contact.deleted"
	crmContactUpdatedDurable = "logistics-crm-contact-updated"
	crmContactDeletedDurable = "logistics-crm-contact-deleted"

	// crmContactRetryDelay spaces out redeliveries while the database or the CRM is failing, so
	// an outage does not use up the durable's deliveries at once.
	crmContactRetryDelay = 30 * time.Second
	crmContactMaxDeliver = 5
)

// ContactFetcher reads one CRM contact; *marketflow.Client implements it.
type ContactFetcher interface {
	GetContact(ctx context.Context, tenantID, contactID uuid.UUID) (marketflow.Contact, error)
}

// contactTask is what the consumer needs to know about one task naming the contact.
type contactTask struct {
	Status   string
	Metadata map[string]any
	// DropoffContact: a drop-off step still holds a contact name or phone.
	DropoffContact bool
}

// rewriteResult counts what a RewriteContact changed.
type rewriteResult struct {
	Tasks int // tasks whose metadata was saved
	Steps int // drop-off steps whose contact name and phone were cleared
}

// taskMetadataStore is the part of the tasks table the consumer uses; entTaskStore implements it.
type taskMetadataStore interface {
	// ContactTasks returns every task, in any tenant, naming the contact.
	ContactTasks(ctx context.Context, contactID uuid.UUID) ([]contactTask, error)
	// RewriteContact, in one transaction over every task naming the contact: applies edit to a
	// copy of the metadata and saves the ones edit changed; when clearFinishedSteps is set, also
	// clears contact_name and contact_phone on the drop-off steps of the tasks that are finished
	// (tasks.IsTerminal). Steps of an active task are never touched.
	RewriteContact(ctx context.Context, contactID uuid.UUID, edit func(map[string]any) bool, clearFinishedSteps bool) (rewriteResult, error)
}

// CRMContactConsumer keeps the customer copy on delivery tasks in step with marketflow, the
// customer master. A task keeps crm_contact_id plus customer_name, customer_phone and
// customer_email in its metadata (tasks.withCustomerMetadata), and its drop-off step keeps the
// customer's name and phone for the rider (task_steps.contact_name, contact_phone).
//
//   - crm.contact.deleted ({contact_id}): crm_contact_id and the three copies are removed from
//     every task that names the contact.
//   - crm.contact.updated ({contact_id, fields}): marketflow publishes ids only and does not say
//     why the contact changed, so when a task still carries a copy for the contact the consumer
//     reads it back from the CRM. Erased (first name "Erased" and no last name, email or phone;
//     see marketflow.Contact.Erased): the three copies are removed and the link is kept. Otherwise
//     each copy the task already holds is refreshed to the CRM's value when the CRM has one; a
//     copy the task does not hold is never added.
//   - On a delete and on an erasure, the drop-off step's contact name and phone are cleared too,
//     but only on finished tasks (tasks.TerminalStatuses: delivered, completed, cancelled, failed,
//     returned). An active task keeps them, so a rider mid-delivery can still call the customer;
//     the metadata copies are removed either way. A task finishing after the event keeps its step
//     contact until the next delete or erasure event for the contact.
//
// Tasks are matched in every tenant on the canonical lower-case contact id: CRM contact ids are
// random UUIDs minted by marketflow, so a match anywhere is the same person (the crm-links check
// makes the same choice). Both handlers are idempotent. Logs carry ids and counts only.
type CRMContactConsumer struct {
	store taskMetadataStore
	crm   ContactFetcher
	log   *zap.Logger
}

// NewCRMContactConsumer builds the consumer over the tasks table. crm may be a disabled
// *marketflow.Client: updates are then retried until the durable gives up, and deletes still apply.
func NewCRMContactConsumer(log *zap.Logger, db *ent.Client, crm ContactFetcher) *CRMContactConsumer {
	if log == nil {
		log = zap.NewNop()
	}
	return &CRMContactConsumer{store: entTaskStore{db: db}, crm: crm, log: log.Named("consumers.crm_contact")}
}

// Start binds the two durables on the stream that holds crm.> (creating a "crm" stream when none
// does yet, as pos-api and projects-api do). Blocks until ctx is done, so run it in a goroutine.
func (c *CRMContactConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	stream, err := crmStream(js)
	if err != nil {
		return fmt.Errorf("crm contact events: %w", err)
	}
	opts := func(durable string) []nats.SubOpt {
		return []nats.SubOpt{
			nats.Durable(durable),
			nats.AckExplicit(),
			nats.AckWait(30 * time.Second),
			nats.MaxDeliver(crmContactMaxDeliver),
			// Replays what is still in the stream on first start, so an erasure from the last
			// few days is applied too; both handlers are idempotent.
			nats.DeliverAll(),
		}
	}
	eventslib.SubscribeQueueWithRebind(c.log, js, stream, crmContactUpdatedSubject, crmContactUpdatedDurable,
		c.wrap(c.handleUpdated), opts(crmContactUpdatedDurable)...)
	eventslib.SubscribeQueueWithRebind(c.log, js, stream, crmContactDeletedSubject, crmContactDeletedDurable,
		c.wrap(c.handleDeleted), opts(crmContactDeletedDurable)...)
	c.log.Info("crm contact consumer started", zap.String("stream", stream))

	<-ctx.Done()
	return nil
}

// crmStream finds the stream that holds crm.contact.updated, or creates "crm" for crm.>.
func crmStream(js nats.JetStreamContext) (string, error) {
	if name, err := js.StreamNameBySubject(crmContactUpdatedSubject); err == nil && name != "" {
		return name, nil
	}
	if _, err := js.AddStream(&nats.StreamConfig{
		Name:      "crm",
		Subjects:  []string{"crm.>"},
		Retention: nats.LimitsPolicy,
		MaxAge:    72 * time.Hour,
		Storage:   nats.FileStorage,
	}); err != nil && !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
		if name, lookupErr := js.StreamNameBySubject(crmContactUpdatedSubject); lookupErr == nil && name != "" {
			return name, nil
		}
		return "", fmt.Errorf("ensure crm stream: %w", err)
	}
	return "crm", nil
}

// settleMsg is the part of *nats.Msg wrap uses.
type settleMsg interface {
	Ack(opts ...nats.AckOpt) error
	NakWithDelay(delay time.Duration, opts ...nats.AckOpt) error
}

// wrap decodes the event and settles the message from the handler's result: acked on success or
// when the event cannot be read (a retry would not help), redelivered after crmContactRetryDelay
// on error. The server stops redelivering at crmContactMaxDeliver.
func (c *CRMContactConsumer) wrap(h func(context.Context, *eventslib.Event) error) nats.MsgHandler {
	return func(msg *nats.Msg) { c.settle(msg, msg.Data, h) }
}

func (c *CRMContactConsumer) settle(m settleMsg, data []byte, h func(context.Context, *eventslib.Event) error) {
	evt, err := eventslib.FromJSON(data)
	if err != nil {
		c.log.Warn("crm contact event: unreadable, acking")
		_ = m.Ack()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h(ctx, evt); err != nil {
		c.log.Warn("crm contact event: apply failed, retrying later",
			zap.String("tenant_id", evt.TenantID.String()), zap.String("contact_id", payloadContactID(evt).String()))
		_ = m.NakWithDelay(crmContactRetryDelay)
		return
	}
	_ = m.Ack()
}

func (c *CRMContactConsumer) handleDeleted(ctx context.Context, evt *eventslib.Event) error {
	id := payloadContactID(evt)
	if id == uuid.Nil {
		return nil
	}
	_, err := c.ContactDeleted(ctx, id)
	return err
}

func (c *CRMContactConsumer) handleUpdated(ctx context.Context, evt *eventslib.Event) error {
	id := payloadContactID(evt)
	if id == uuid.Nil || evt.TenantID == uuid.Nil {
		return nil
	}
	_, err := c.ContactUpdated(ctx, evt.TenantID, id)
	return err
}

// payloadContactID reads contact_id from the payload, falling back to the event's aggregate id
// (marketflow sets both to the contact). uuid.Nil when neither is a UUID.
func payloadContactID(evt *eventslib.Event) uuid.UUID {
	if s, ok := evt.Payload["contact_id"].(string); ok {
		if id, err := uuid.Parse(strings.TrimSpace(s)); err == nil {
			return id
		}
	}
	return evt.AggregateID
}

// snapshotKeys are the customer copies a task keeps next to crm_contact_id.
var snapshotKeys = []string{tasks.MetaCustomerName, tasks.MetaCustomerPhone, tasks.MetaCustomerEmail}

// ContactDeleted removes crm_contact_id and the customer copies from every task, in any tenant,
// that names the contact, and clears the drop-off contact of those that are finished. It answers
// what changed (nothing on a repeat delivery).
func (c *CRMContactConsumer) ContactDeleted(ctx context.Context, contactID uuid.UUID) (rewriteResult, error) {
	res, err := c.store.RewriteContact(ctx, contactID, func(meta map[string]any) bool {
		delete(meta, tasks.MetaCRMContactID)
		for _, k := range snapshotKeys {
			delete(meta, k)
		}
		return true
	}, true)
	if err != nil {
		return rewriteResult{}, fmt.Errorf("unlink deleted contact: %w", err)
	}
	if res.Tasks > 0 || res.Steps > 0 {
		c.log.Info("crm contact deleted: tasks unlinked", zap.String("contact_id", contactID.String()),
			zap.Int("tasks", res.Tasks), zap.Int("steps_cleared", res.Steps))
	}
	return res, nil
}

// ContactUpdated applies the CRM's current view of the contact to every task naming it: an erased
// contact's copies are removed and the finished tasks' drop-off contact cleared (the link stays),
// a live contact's copies are refreshed. A contact no task holds anything for, and a contact the
// CRM no longer knows (its delete event unlinks it), change nothing. An error means the CRM or the
// database could not answer and the event should be retried.
func (c *CRMContactConsumer) ContactUpdated(ctx context.Context, tenantID, contactID uuid.UUID) (rewriteResult, error) {
	ts, err := c.store.ContactTasks(ctx, contactID)
	if err != nil {
		return rewriteResult{}, fmt.Errorf("find tasks: %w", err)
	}
	if !anyHasSnapshot(ts) {
		return rewriteResult{}, nil
	}
	contact, err := c.crm.GetContact(ctx, tenantID, contactID)
	if errors.Is(err, marketflow.ErrContactNotFound) {
		return rewriteResult{}, nil
	}
	if err != nil {
		return rewriteResult{}, fmt.Errorf("read crm contact: %w", err)
	}
	erased := contact.Erased()
	res, err := c.store.RewriteContact(ctx, contactID, func(meta map[string]any) bool {
		if erased {
			return clearSnapshot(meta)
		}
		return refreshSnapshot(meta, contact)
	}, erased)
	if err != nil {
		return rewriteResult{}, fmt.Errorf("apply contact update: %w", err)
	}
	if res.Tasks > 0 || res.Steps > 0 {
		msg := "crm contact updated: task copies refreshed"
		if erased {
			msg = "crm contact erased: task copies removed"
		}
		c.log.Info(msg, zap.String("tenant_id", tenantID.String()), zap.String("contact_id", contactID.String()),
			zap.Int("tasks", res.Tasks), zap.Int("steps_cleared", res.Steps))
	}
	return res, nil
}

// anyHasSnapshot reports whether any task still holds something an update may change: a metadata
// copy, or a drop-off contact on a finished task.
func anyHasSnapshot(ts []contactTask) bool {
	for _, t := range ts {
		if t.DropoffContact && tasks.IsTerminal(t.Status) {
			return true
		}
		for _, k := range snapshotKeys {
			if s, _ := t.Metadata[k].(string); strings.TrimSpace(s) != "" {
				return true
			}
		}
	}
	return false
}

// clearSnapshot removes the customer copies and keeps crm_contact_id. It reports whether anything
// was removed.
func clearSnapshot(meta map[string]any) bool {
	changed := false
	for _, k := range snapshotKeys {
		if _, ok := meta[k]; ok {
			delete(meta, k)
			changed = true
		}
	}
	return changed
}

// refreshSnapshot sets each customer copy the task already holds to the CRM's value, when the CRM
// has one. A copy the task does not hold is not added. It reports whether anything changed.
func refreshSnapshot(meta map[string]any, contact marketflow.Contact) bool {
	changed := false
	for _, kv := range [][2]string{
		{tasks.MetaCustomerName, contact.Name()},
		{tasks.MetaCustomerPhone, strings.TrimSpace(contact.Phone)},
		{tasks.MetaCustomerEmail, strings.TrimSpace(contact.Email)},
	} {
		if kv[1] == "" {
			continue
		}
		cur, held := meta[kv[0]].(string)
		if held && cur != kv[1] {
			meta[kv[0]] = kv[1]
			changed = true
		}
	}
	return changed
}

// entTaskStore is taskMetadataStore over the tasks table.
type entTaskStore struct{ db *ent.Client }

// dropoffWithContact matches drop-off steps that still hold a contact name or phone.
func dropoffWithContact() predicate.TaskStep {
	return taskstep.And(
		taskstep.StepType("dropoff"),
		taskstep.Or(
			taskstep.And(taskstep.ContactNameNotNil(), taskstep.ContactNameNEQ("")),
			taskstep.And(taskstep.ContactPhoneNotNil(), taskstep.ContactPhoneNEQ("")),
		),
	)
}

func (s entTaskStore) ContactTasks(ctx context.Context, contactID uuid.UUID) ([]contactTask, error) {
	ts, err := s.db.Task.Query().Where(tasks.MetadataContact(contactID)).
		WithSteps(func(q *ent.TaskStepQuery) { q.Where(dropoffWithContact()) }).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]contactTask, 0, len(ts))
	for _, t := range ts {
		out = append(out, contactTask{Status: t.Status, Metadata: t.Metadata, DropoffContact: len(t.Edges.Steps) > 0})
	}
	return out, nil
}

func (s entTaskStore) RewriteContact(ctx context.Context, contactID uuid.UUID, edit func(map[string]any) bool, clearFinishedSteps bool) (rewriteResult, error) {
	tx, err := s.db.Tx(ctx)
	if err != nil {
		return rewriteResult{}, err
	}
	ts, err := tx.Task.Query().Where(tasks.MetadataContact(contactID)).All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return rewriteResult{}, err
	}
	var res rewriteResult
	for _, t := range ts {
		if clearFinishedSteps && tasks.IsTerminal(t.Status) {
			n, err := tx.TaskStep.Update().
				Where(taskstep.TaskID(t.ID), dropoffWithContact()).
				ClearContactName().ClearContactPhone().
				Save(ctx)
			if err != nil {
				_ = tx.Rollback()
				return rewriteResult{}, err
			}
			res.Steps += n
		}
		meta := make(map[string]any, len(t.Metadata))
		for k, v := range t.Metadata {
			meta[k] = v
		}
		if !edit(meta) {
			continue
		}
		if err := tx.Task.UpdateOneID(t.ID).SetMetadata(meta).Exec(ctx); err != nil {
			_ = tx.Rollback()
			return rewriteResult{}, err
		}
		res.Tasks++
	}
	if err := tx.Commit(); err != nil {
		return rewriteResult{}, err
	}
	return res, nil
}
