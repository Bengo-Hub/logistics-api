package tasks

import (
	"testing"

	"github.com/bengobox/logistics-service/internal/ent"
)

func TestDescribeItems(t *testing.T) {
	desc, count := describeItems([]map[string]interface{}{
		{"name": "Burger", "quantity": 2.0},
		{"name": "Chips", "quantity": 1},
		{"name": "", "quantity": 5.0}, // unnamed lines are skipped
		{"name": "Soda"},              // no quantity counts as one
	})
	if desc != "2x Burger, 1x Chips, 1x Soda" || count != 4 {
		t.Fatalf("got %q / %d", desc, count)
	}
	if desc, count := describeItems(nil); desc != "" || count != 0 {
		t.Fatalf("empty input: %q / %d", desc, count)
	}
}

func TestTaskCODAmount(t *testing.T) {
	if got := taskCODAmount(&ent.Task{CashOnDelivery: 500}); got != 500 {
		t.Fatalf("column value: %v", got)
	}
	if got := taskCODAmount(&ent.Task{Metadata: map[string]any{"cash_on_delivery": 850.0}}); got != 850 {
		t.Fatalf("order-created tasks carry COD in metadata: %v", got)
	}
	if got := taskCODAmount(&ent.Task{}); got != 0 {
		t.Fatalf("prepaid task: %v", got)
	}
}

func TestPODAllowedOnlyAfterPickup(t *testing.T) {
	for _, s := range []string{"pending", "assigned", "accepted", "en_route_pickup", "arrived_pickup"} {
		if podAllowedFrom[s] {
			t.Fatalf("%s: the order is still at the outlet", s)
		}
	}
	for _, s := range []string{"picked_up", "en_route_dropoff", "arrived_dropoff", "en_route"} {
		if !podAllowedFrom[s] {
			t.Fatalf("%s: the rider has the order", s)
		}
	}
}
