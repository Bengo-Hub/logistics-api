package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bengobox/logistics-service/internal/ent"
)

// The create endpoints (POST .../tasks and the S2S dispatch create) returned the stored task
// as is, so the caller read back the customer's email, CRM contact id and delivery code that the
// list and get DTO hide.
func TestCreatedTaskResponseHidesSecretMetadata(t *testing.T) {
	stored := &ent.Task{TrackingCode: "TRK1", Metadata: map[string]any{
		"pod_code": "123456", "customer_email": "amina@example.com",
		"crm_contact_id": "6f1c2d3e-0000-4000-8000-000000000001", "customer_phone": "0712000111",
		"customer_name": "Amina", "order_number": "POS-1",
	}}
	rec := httptest.NewRecorder()
	respondJSON(rec, http.StatusCreated, createdTaskResponse(stored))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		TrackingCode string         `json:"tracking_code"`
		Metadata     map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	for _, k := range []string{"pod_code", "customer_email", "crm_contact_id", "customer_phone"} {
		if _, leaked := body.Metadata[k]; leaked {
			t.Fatalf("%s leaked in the create response: %s", k, rec.Body.String())
		}
	}
	if body.TrackingCode != "TRK1" || body.Metadata["order_number"] != "POS-1" || body.Metadata["customer_name"] != "Amina" {
		t.Fatalf("the rest of the task must be returned: %s", rec.Body.String())
	}
	if stored.Metadata["pod_code"] != "123456" || stored.Metadata["customer_email"] != "amina@example.com" {
		t.Fatal("the stored task must not be changed")
	}
	if createdTaskResponse(nil) != nil {
		t.Fatal("nil task must stay nil")
	}
}

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

// The customer's email and CRM id are kept for delivery emails only; the rider app and the
// dispatch board never get them.
func TestPublicTaskMetadataHidesCustomerEmailAndCRMID(t *testing.T) {
	out := publicTaskMetadata(map[string]any{
		"customer_email": "amina@example.com", "crm_contact_id": "6f1c2d3e-0000-4000-8000-000000000001",
		"customer_name": "Amina", "order_number": "ORD-1",
	})
	for _, k := range []string{"customer_email", "crm_contact_id"} {
		if _, leaked := out[k]; leaked {
			t.Fatalf("%s leaked: %v", k, out)
		}
	}
	if out["customer_name"] != "Amina" || out["order_number"] != "ORD-1" {
		t.Fatalf("other metadata must be kept: %v", out)
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
