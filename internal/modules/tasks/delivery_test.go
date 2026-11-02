package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
)

func TestCodeMatches_ServerSideCheck(t *testing.T) {
	cases := []struct {
		stored, entered string
		want            bool
	}{
		{"482913", "482913", true},
		{"482913", " 482 913 ", true},
		{"482913", "482-913", true},
		{"482913", "482914", false},
		{"482913", "", false},
		{"", "", false},
		{"", "123456", false},
		{"482913", "48291", false},
	}
	for _, c := range cases {
		if got := codeMatches(c.stored, c.entered); got != c.want {
			t.Errorf("codeMatches(%q, %q) = %v, want %v", c.stored, c.entered, got, c.want)
		}
	}
}

func TestCodeAttemptsReadFromMetadata(t *testing.T) {
	if n := codeAttempts(&ent.Task{Metadata: map[string]any{codeAttemptsKey: float64(3)}}); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
	if n := codeAttempts(&ent.Task{}); n != 0 {
		t.Fatalf("attempts on a fresh task = %d", n)
	}
}

func TestCheckCOD(t *testing.T) {
	// Prepaid: nothing is recorded, even if the client sent a tender.
	c, err := checkCOD(0, 500, "mpesa", "ABC")
	if err != nil || c.Method != "" || c.collected() {
		t.Fatalf("prepaid order recorded a tender: %+v %v", c, err)
	}
	if _, err := checkCOD(1200, 1000, "cash", ""); err == nil {
		t.Fatal("short cash must be refused")
	}
	if _, err := checkCOD(1200, 1200, "card", ""); err == nil {
		t.Fatal("unknown tender must be refused")
	}
	if _, err := checkCOD(1200, 1200, "mpesa", "SGH7K2"); err == nil {
		t.Fatal("M-Pesa without a full code must be refused")
	}
	c, err = checkCOD(1200, 1200, "mpesa", "sgh7 k2l9qp")
	if err != nil || c.Method != "mpesa" || c.Reference != "SGH7K2L9QP" || !c.collected() {
		t.Fatalf("mpesa collection = %+v %v", c, err)
	}
	c, err = checkCOD(1200, 1500, "", "IGNORED123")
	if err != nil || c.Method != "cash" || c.Reference != "" || c.Amount != 1500 {
		t.Fatalf("cash default = %+v %v", c, err)
	}
}

func TestOverrideNeedsReason(t *testing.T) {
	s := NewService(nil, zap.NewNop())
	for _, reason := range []string{"", "   "} {
		_, err := s.OverrideDelivery(context.Background(), uuid.New(), uuid.New(), OverrideDeliveryRequest{Reason: reason}, Actor{Type: "dispatcher"})
		if !errors.Is(err, ErrReasonRequired) {
			t.Fatalf("override with reason %q: got %v, want ErrReasonRequired", reason, err)
		}
	}
}

func TestStatusChange_NoBareAssignment(t *testing.T) {
	if err := statusChangeError("pending", "assigned", false, ""); !errors.Is(err, ErrUseAssignment) {
		t.Fatalf("dispatcher pending to assigned by status: got %v", err)
	}
	if err := statusChangeError("accepted", "pending", false, ""); !errors.Is(err, ErrUseAssignment) {
		t.Fatalf("dispatcher back to pending by status: got %v", err)
	}
}

func TestStatusChange_RiderFailsOnlyAfterPickup(t *testing.T) {
	for _, from := range []string{"en_route_pickup", "arrived_pickup"} {
		if err := statusChangeError(from, "failed", true, "bike broke"); !errors.Is(err, ErrDeclineBeforePickup) {
			t.Fatalf("rider fail from %s: got %v", from, err)
		}
	}
	for _, from := range []string{"picked_up", "en_route_dropoff", "arrived_dropoff", "en_route"} {
		if err := statusChangeError(from, "failed", true, "customer not answering"); err != nil {
			t.Fatalf("rider fail from %s: %v", from, err)
		}
	}
	// A dispatcher may still close a pre-pickup job as failed.
	if err := statusChangeError("arrived_pickup", "failed", false, "outlet closed"); err != nil {
		t.Fatalf("dispatcher fail before pickup: %v", err)
	}
}

func TestStatusChange_NoSkippingTheOutlet(t *testing.T) {
	if err := statusChangeError("accepted", "en_route", true, ""); err == nil {
		t.Fatal("accepted straight to en_route skips the pickup and must be refused")
	}
	if err := statusChangeError("accepted", "picked_up", false, ""); err == nil {
		t.Fatal("accepted straight to picked_up must be refused")
	}
	// A task already on the legacy en_route status can still reach the customer step.
	if err := statusChangeError("en_route", "arrived_dropoff", true, ""); err != nil {
		t.Fatalf("en_route to arrived_dropoff: %v", err)
	}
}
