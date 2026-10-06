package tasks

import (
	"errors"
	"testing"
	"time"
)

func TestStatusChange_RiderCannotCancelCustomersDelivery(t *testing.T) {
	if err := statusChangeError("accepted", "cancelled", true, "too far"); !errors.Is(err, ErrRiderMayNotSetStatus) {
		t.Fatalf("rider cancel: got %v, want ErrRiderMayNotSetStatus", err)
	}
}

func TestStatusChange_DispatcherMayCancelWithReason(t *testing.T) {
	if err := statusChangeError("en_route_pickup", "cancelled", false, "customer called"); err != nil {
		t.Fatalf("dispatcher cancel: %v", err)
	}
	if err := statusChangeError("en_route_pickup", "cancelled", false, " "); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("cancel without reason: got %v", err)
	}
}

func TestStatusChange_FailedDeliveryNeedsReason(t *testing.T) {
	if err := statusChangeError("arrived_dropoff", "failed", true, ""); !errors.Is(err, ErrReasonRequired) {
		t.Fatalf("failed without reason: got %v", err)
	}
	if err := statusChangeError("arrived_dropoff", "failed", true, "customer not answering"); err != nil {
		t.Fatalf("failed with reason: %v", err)
	}
}

func TestStatusChange_DeliveredOnlyThroughProof(t *testing.T) {
	for _, to := range []string{"delivered", "completed"} {
		if err := statusChangeError("arrived_dropoff", to, false, ""); err == nil {
			t.Fatalf("%s via status change should be refused", to)
		}
	}
}

func TestStatusChange_ClosedTaskNeverMoves(t *testing.T) {
	for _, from := range []string{"delivered", "failed", "cancelled"} {
		if err := statusChangeError(from, "en_route_pickup", false, ""); !errors.Is(err, ErrTaskClosed) {
			t.Fatalf("from %s: got %v, want ErrTaskClosed", from, err)
		}
	}
}

func TestStatusChange_RiderLegsFollowTheStateMachine(t *testing.T) {
	legs := [][2]string{
		{"assigned", "accepted"}, {"accepted", "en_route_pickup"}, {"en_route_pickup", "arrived_pickup"},
		{"arrived_pickup", "picked_up"}, {"picked_up", "en_route_dropoff"}, {"en_route_dropoff", "arrived_dropoff"},
	}
	for _, l := range legs {
		if err := statusChangeError(l[0], l[1], true, ""); err != nil {
			t.Fatalf("%s to %s: %v", l[0], l[1], err)
		}
	}
	if err := statusChangeError("accepted", "arrived_dropoff", true, ""); err == nil {
		t.Fatal("skipping legs should be refused")
	}
	if err := statusChangeError("assigned", "pending", true, ""); !errors.Is(err, ErrRiderMayNotSetStatus) {
		t.Fatalf("rider resetting to pending: got %v", err)
	}
}

func TestReasonMetadata(t *testing.T) {
	at := time.Date(2026, 10, 6, 9, 30, 0, 0, time.UTC)
	m := reasonMetadata("failed", "gate locked", at)
	if m["failure_reason"] != "gate locked" || m["failed_at"] != "2026-10-06T09:30:00Z" {
		t.Fatalf("failed stamp = %v", m)
	}
	if m := reasonMetadata("cancelled", "order cancelled", at); m["cancellation_reason"] != "order cancelled" {
		t.Fatalf("cancel stamp = %v", m)
	}
	if m := reasonMetadata("picked_up", "", at); m["picked_up_at"] == nil {
		t.Fatal("pickup time not stamped")
	}
	if m := reasonMetadata("arrived_pickup", "", at); len(m) != 0 {
		t.Fatalf("unexpected stamp %v", m)
	}
}

func TestMergeMetaKeepsOrderDetails(t *testing.T) {
	base := map[string]any{"order_number": "DL-1", "pod_code": "123456"}
	out := mergeMeta(base, map[string]any{"failure_reason": "x"})
	if out["order_number"] != "DL-1" || out["pod_code"] != "123456" || out["failure_reason"] != "x" {
		t.Fatalf("merged = %v", out)
	}
	if _, touched := base["failure_reason"]; touched {
		t.Fatal("base map must not be modified")
	}
}

func TestPrePickupDecidesWhoCanHandBack(t *testing.T) {
	for _, s := range []string{"assigned", "accepted", "en_route_pickup", "arrived_pickup"} {
		if !IsPrePickup(s) {
			t.Fatalf("%s should allow decline", s)
		}
	}
	for _, s := range []string{"picked_up", "en_route_dropoff", "arrived_dropoff", "pending", "delivered"} {
		if IsPrePickup(s) {
			t.Fatalf("%s must not allow decline", s)
		}
	}
	if !IsTerminal("delivered") || IsTerminal("picked_up") {
		t.Fatal("terminal set wrong: delivered must be closed (the SLA monitor treated it as open)")
	}
}

func TestPodPhotoURL_InlineDataNotStored(t *testing.T) {
	if url, inline := podPhotoURL("data:image/jpeg;base64,/9j/4AAQ"); url != "" || !inline {
		t.Fatalf("data url: got %q inline=%v", url, inline)
	}
	if url, inline := podPhotoURL("https://logisticsapi.example/media/uploads/pod/a.jpg"); url == "" || inline {
		t.Fatalf("uploaded url dropped: %q", url)
	}
}

func TestOrderStepsWithoutCoordinates(t *testing.T) {
	// An outlet with no map pin still gives the rider a name, address and phone.
	if !hasPickup(CreateTaskFromOrderRequest{PickupName: "Westlands", PickupPhone: "0700000000"}) {
		t.Fatal("pickup step skipped for an outlet without coordinates")
	}
	if !hasDropoff(CreateTaskFromOrderRequest{CustomerName: "Amina", CustomerPhone: "0711"}) {
		t.Fatal("dropoff step skipped for an address without coordinates")
	}
	if hasPickup(CreateTaskFromOrderRequest{}) || hasDropoff(CreateTaskFromOrderRequest{}) {
		t.Fatal("empty request should create no steps")
	}
}
