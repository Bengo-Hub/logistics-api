package consumers

import (
	"testing"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
)

func TestCancelledOrderRef(t *testing.T) {
	tenant, order := uuid.New(), uuid.New()
	evt := &eventslib.Event{TenantID: tenant, Payload: map[string]interface{}{
		"order_id": order.String(), "reason": "customer changed mind",
	}}
	tid, ref, reason, err := cancelledOrderRef(evt)
	if err != nil {
		t.Fatal(err)
	}
	if tid != tenant || ref != "order:"+order.String() {
		t.Fatalf("ref = %s %s; tasks are keyed order:<uuid>", tid, ref)
	}
	if reason != "order cancelled: customer changed mind" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestCancelledOrderRefRejectsBadEvents(t *testing.T) {
	if _, _, _, err := cancelledOrderRef(&eventslib.Event{Payload: map[string]interface{}{"order_id": uuid.NewString()}}); err == nil {
		t.Fatal("missing tenant accepted")
	}
	if _, _, _, err := cancelledOrderRef(&eventslib.Event{TenantID: uuid.New(), Payload: map[string]interface{}{"order_id": "x"}}); err == nil {
		t.Fatal("bad order id accepted")
	}
	_, _, reason, _ := cancelledOrderRef(&eventslib.Event{TenantID: uuid.New(), Payload: map[string]interface{}{"order_id": uuid.NewString()}})
	if reason != "order cancelled" {
		t.Fatalf("default reason = %q", reason)
	}
}

func TestJoinNotes(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{"", "Blue gate", "Blue gate"},
		{"No onions", "", "No onions"},
		{"No onions", "Blue gate", "No onions. Blue gate"},
		{"Blue gate, 3rd floor", "Blue gate", "Blue gate, 3rd floor"},
	}
	for _, c := range cases {
		if got := joinNotes(c.a, c.b); got != c.want {
			t.Fatalf("joinNotes(%q,%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestBuildAddressLabelIncludesSecondLine(t *testing.T) {
	got := buildAddressLabel(map[string]interface{}{"address_line1": "Plot 9", "address_line2": "Apt 4B", "city": "Nairobi"})
	if got != "Plot 9, Apt 4B, Nairobi" {
		t.Fatalf("label = %q", got)
	}
}
