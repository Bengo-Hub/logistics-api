package handlers

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
)

func TestKPIFromCounts(t *testing.T) {
	rows := []struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}{
		{"pending", 2}, {"assigned", 1}, {"en_route_dropoff", 3}, {"delivered", 10},
		{"completed", 1}, {"failed", 2}, {"cancelled", 4},
	}
	k := kpiFromCounts(rows)
	if k.total != 23 || k.pending != 2 || k.active != 4 || k.completed != 11 || k.failed != 2 || k.cancelled != 4 {
		t.Fatalf("kpi = %+v", k)
	}
}

func TestTaskResponseReasonsAndCurrentRider(t *testing.T) {
	declined, current := uuid.New(), uuid.New()
	now := time.Now()
	tk := &ent.Task{
		ID: uuid.New(), Status: "en_route_pickup",
		Metadata: map[string]any{"failure_reason": "", "cancellation_reason": "", "picked_up_at": "2026-10-06T10:00:00Z"},
	}
	tk.Edges.Assignments = []*ent.TaskAssignment{
		{FleetMemberID: declined, Status: "declined", AssignedAt: now},
		{FleetMemberID: current, Status: "accepted", AssignedAt: now.Add(-time.Minute)},
	}
	resp := toTaskResponse(tk)
	if resp.AssignedRiderID == nil || *resp.AssignedRiderID != current.String() {
		t.Fatalf("assigned rider = %v, want the rider holding the job, not the one who declined", resp.AssignedRiderID)
	}
	if resp.PickedUpAt == nil {
		t.Fatal("picked_up_at not read from metadata")
	}
}

func TestPublicMetadataHidesDeliveryCode(t *testing.T) {
	out := publicTaskMetadata(map[string]any{"pod_code": "123456", "order_number": "DL-9"})
	if _, ok := out["pod_code"]; ok || out["order_number"] != "DL-9" {
		t.Fatalf("metadata = %v", out)
	}
}
