package tasks

import (
	"testing"
	"time"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/platform/events"
)

func TestWithCustomerMetadataKeepsExistingAndSkipsBlank(t *testing.T) {
	meta := withCustomerMetadata(map[string]any{"customer_email": "set@example.com"}, customerIdentity{
		Email: "other@example.com", Name: " Amina ", Phone: "",
	})
	if meta["customer_email"] != "set@example.com" {
		t.Fatalf("a value already in the metadata must win: %v", meta)
	}
	if meta["customer_name"] != "Amina" {
		t.Fatalf("name not trimmed or not kept: %v", meta)
	}
	if _, ok := meta["customer_phone"]; ok {
		t.Fatalf("blank phone must be skipped: %v", meta)
	}
	if got := withCustomerMetadata(nil, customerIdentity{}); got != nil {
		t.Fatalf("nothing to add must leave nil metadata nil: %v", got)
	}
}

func TestWithCustomerFillsEventFromTask(t *testing.T) {
	// A due time still ahead: a past one is not shown (TestEstimatedArrivalOnlyInTheFuture).
	due := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	tk := &ent.Task{
		Metadata: map[string]any{"customer_email": "amina@example.com"},
		SLADueAt: &due,
	}
	tk.Edges.Steps = []*ent.TaskStep{
		{StepType: "pickup", ContactName: "Westlands outlet", ContactPhone: "0700000000"},
		{StepType: "dropoff", ContactName: "Amina Wanjiru", ContactPhone: "0712345678"},
	}
	d := withCustomer(events.TaskEventData{TaskID: "t1", Status: "assigned"}, tk)
	if d.CustomerEmail != "amina@example.com" || d.CustomerName != "Amina Wanjiru" || d.CustomerPhone != "0712345678" {
		t.Fatalf("customer = %+v", d)
	}
	if d.EstimatedArrival != due.Format(time.RFC3339) {
		t.Fatalf("estimated arrival = %q", d.EstimatedArrival)
	}

	linked := withCustomer(events.TaskEventData{TaskID: "t2"}, &ent.Task{Metadata: map[string]any{
		"crm_contact_id": "6f1c2d3e-0000-4000-8000-000000000001",
	}})
	if linked.CRMContactID != "6f1c2d3e-0000-4000-8000-000000000001" || linked.EstimatedArrival != "" {
		t.Fatalf("linked = %+v", linked)
	}
	if got := withCustomer(events.TaskEventData{TaskID: "t3"}, nil); got.TaskID != "t3" {
		t.Fatalf("nil task must leave the event as it was: %+v", got)
	}
}

// A due time that has passed would tell the customer the order should already be there, so the
// estimated arrival shows only while it is still ahead.
func TestEstimatedArrivalOnlyInTheFuture(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	past := now.Add(-15 * time.Minute)
	ahead := now.Add(45 * time.Minute)
	later := now.Add(90 * time.Minute)

	if got := estimatedArrival(&ent.Task{SLADueAt: &past}, now); got != "" {
		t.Fatalf("past SLA due time shown: %q", got)
	}
	if got := estimatedArrival(&ent.Task{SLADueAt: &now}, now); got != "" {
		t.Fatalf("a due time of exactly now is not ahead: %q", got)
	}
	var zero time.Time
	if got := estimatedArrival(&ent.Task{RequestedDropoffAt: &zero}, now); got != "" {
		t.Fatalf("zero drop-off time shown: %q", got)
	}
	if got := estimatedArrival(&ent.Task{RequestedDropoffAt: &ahead, SLADueAt: &later}, now); got != "2026-11-12T10:45:00Z" {
		t.Fatalf("requested drop-off ahead must win: %q", got)
	}
	// A requested drop-off that has passed falls back to an SLA due time still ahead.
	if got := estimatedArrival(&ent.Task{RequestedDropoffAt: &past, SLADueAt: &later}, now); got != "2026-11-12T11:30:00Z" {
		t.Fatalf("past drop-off must fall back to the SLA due time: %q", got)
	}
	if got := estimatedArrival(&ent.Task{}, now); got != "" {
		t.Fatalf("no due time: %q", got)
	}
}

// A crm_contact_id that is not a UUID could never be looked up, and with it on the task the
// events would drop the email; it is skipped so the email is kept and reaches the customer.
func TestWithCustomerMetadataSkipsInvalidCRMContactID(t *testing.T) {
	meta := withCustomerMetadata(nil, customerIdentity{
		CRMContactID: "cust-42", Email: "amina@example.com", Name: "Amina",
	})
	if _, ok := meta["crm_contact_id"]; ok {
		t.Fatalf("invalid crm_contact_id kept: %v", meta)
	}
	if meta["customer_email"] != "amina@example.com" {
		t.Fatalf("email must be kept: %v", meta)
	}
	d := withCustomer(events.TaskEventData{TaskID: "t1"}, &ent.Task{Metadata: meta})
	if d.CRMContactID != "" || d.CustomerEmail != "amina@example.com" {
		t.Fatalf("event must carry the email, not a CRM id: %+v", d)
	}

	valid := withCustomerMetadata(nil, customerIdentity{CRMContactID: " 6f1c2d3e-0000-4000-8000-000000000001 "})
	if valid["crm_contact_id"] != "6f1c2d3e-0000-4000-8000-000000000001" {
		t.Fatalf("valid crm_contact_id must be kept (trimmed): %v", valid)
	}
}

// The customer learns who is bringing the order when the rider heads to them, so the en_route
// legs carry the active rider's name and phone; other status events never do.
func TestStatusEventDataCarriesRiderOnEnRoute(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	tk := &ent.Task{TrackingCode: "TRK1", SourceService: "pos", Metadata: map[string]any{"order_number": "POS-1"}}
	rider := riderContact{Email: "brian@example.com", Name: "Brian Otieno", Phone: "0722000111"}

	for _, st := range []string{"en_route_dropoff", "en_route"} {
		d := statusEventData(tk, "picked_up", st, "", now, rider)
		if d.RiderName != "Brian Otieno" || d.RiderPhone != "0722000111" {
			t.Fatalf("%s: rider missing: %+v", st, d)
		}
		if d.RiderEmail != "" {
			t.Fatalf("%s: the rider's email is not for the customer: %+v", st, d)
		}
		if d.Status != st || d.PreviousStatus != "picked_up" || d.OrderNumber != "POS-1" {
			t.Fatalf("%s: event = %+v", st, d)
		}
	}
	if d := statusEventData(tk, "accepted", "en_route_pickup", "", now, rider); d.RiderName != "" || d.RiderPhone != "" {
		t.Fatalf("en_route_pickup must not carry the rider: %+v", d)
	}
	delivered := statusEventData(tk, "arrived_dropoff", "delivered", "", now, rider)
	if delivered.DeliveredAt != "2026-11-12T10:00:00Z" || delivered.RiderName != "" {
		t.Fatalf("delivered = %+v", delivered)
	}
}
