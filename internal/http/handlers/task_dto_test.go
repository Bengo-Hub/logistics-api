package handlers

import (
	"testing"

	"github.com/bengobox/logistics-service/internal/ent"
)

func TestPublicTaskMetadataHidesPODCode(t *testing.T) {
	src := map[string]any{"pod_code": "123456", "order_number": "ORD-1"}
	out := publicTaskMetadata(src)
	if _, leaked := out["pod_code"]; leaked {
		t.Fatal("the customer's delivery code must never reach the rider app")
	}
	if out["order_number"] != "ORD-1" {
		t.Fatal("other metadata must be kept")
	}
	if src["pod_code"] != "123456" {
		t.Fatal("the stored metadata must not be mutated")
	}
}

func TestPriorityLabel(t *testing.T) {
	cases := map[int]string{0: "normal", 1: "normal", 2: "high", 3: "urgent"}
	for p, want := range cases {
		if got := priorityLabel(p); got != want {
			t.Fatalf("priority %d = %s, want %s", p, got, want)
		}
	}
}

func TestTaskResponseReadsOrderFields(t *testing.T) {
	resp := toTaskResponse(&ent.Task{Metadata: map[string]any{
		"order_number":      "ORD-9",
		"items_description": "2x Burger",
		"item_count":        2.0,
		"cash_on_delivery":  700.0,
		"pod_code":          "999999",
	}})
	if resp.OrderNumber != "ORD-9" || resp.ItemsDescription != "2x Burger" || resp.ItemCount != 2 || resp.CashOnDelivery != 700 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if _, leaked := resp.Metadata["pod_code"]; leaked {
		t.Fatal("pod_code leaked")
	}
}
