package events

import "testing"

// The customer's on-its-way email names the rider from the en_route_dropoff event.
func TestEnRouteDropoffEventCarriesRider(t *testing.T) {
	m := TaskEventData{TaskID: "t", Status: "en_route_dropoff", RiderName: "Brian Otieno", RiderPhone: "0722000111"}.toMap()
	if m["rider_name"] != "Brian Otieno" || m["rider_phone"] != "0722000111" {
		t.Fatalf("payload = %v", m)
	}
}

// ordering reads "reason" on task.cancelled and "failure_reason" on task.failed; the
// notifications delivery_failed template reads failure_reason too. Logistics sent neither.
func TestTaskEventCarriesReasonForEveryConsumer(t *testing.T) {
	m := TaskEventData{TaskID: "t", Status: "failed", Reason: "gate locked", RiderPhone: "0700"}.toMap()
	if m["reason"] != "gate locked" || m["failure_reason"] != "gate locked" {
		t.Fatalf("payload = %v", m)
	}
	if m["rider_phone"] != "0700" {
		t.Fatalf("rider phone missing: %v", m)
	}
	if _, ok := (TaskEventData{TaskID: "t", Status: "accepted"}).toMap()["reason"]; ok {
		t.Fatal("empty reason must be omitted")
	}
}

func TestTaskCompletedCarriesDeliveryDetails(t *testing.T) {
	m := TaskEventData{
		TaskID: "t1", Status: "delivered", CashOnDelivery: 1200, CashCollected: true, AmountCollected: 1200,
		CollectionMethod: "mpesa", CollectionReference: "SGH7K2L9QP",
		DeliveredVia: "override", ConfirmedBy: "dispatcher", RecipientName: "Amina", OverrideReason: "customer lost the code",
	}.toMap()
	// ordering-backend handleTaskCompleted and pos-api applyLogisticsStatus read these keys.
	if m["cash_collected"] != true || m["amount_collected"] != 1200.0 || m["collection_method"] != "mpesa" || m["collection_reference"] != "SGH7K2L9QP" {
		t.Fatalf("COD keys = %v", m)
	}
	if m["delivered_via"] != "override" || m["delivery_override"] != true || m["code_verified"] != false ||
		m["override_reason"] != "customer lost the code" || m["recipient_name"] != "Amina" || m["confirmed_by"] != "dispatcher" {
		t.Fatalf("delivery keys = %v", m)
	}
	code := TaskEventData{TaskID: "t2", Status: "delivered", DeliveredVia: "code", ConfirmedBy: "rider"}.toMap()
	if code["code_verified"] != true || code["delivery_override"] != false {
		t.Fatalf("code delivery keys = %v", code)
	}
}

// notifications-api's delivery consumer writes to the customer from these keys and fills the
// delivery_assigned, delivery_completed and delivery_failed templates from the rest.
func TestTaskEventCarriesCustomerAndTemplateFields(t *testing.T) {
	m := TaskEventData{
		TaskID: "t3", Status: "delivered", ProofPhoto: true, RecipientName: "Amina",
		DeliveredAt: "2026-11-12T09:30:00Z", EstimatedArrival: "2026-11-12T09:45:00Z",
		CustomerEmail: "amina@example.com", CustomerName: "Amina Wanjiru", CustomerPhone: "0712345678",
	}.toMap()
	if m["customer_email"] != "amina@example.com" || m["customer_name"] != "Amina Wanjiru" || m["customer_phone"] != "0712345678" {
		t.Fatalf("unlinked customer keys = %v", m)
	}
	if m["delivered_at"] != "2026-11-12T09:30:00Z" || m["estimated_arrival"] != "2026-11-12T09:45:00Z" || m["proof_photo"] != true {
		t.Fatalf("template keys = %v", m)
	}
	if _, ok := m["crm_contact_id"]; ok {
		t.Fatalf("no CRM contact, so no crm_contact_id: %v", m)
	}

	// A CRM-linked customer goes out by id only; the CRM holds the contact details.
	linked := TaskEventData{
		TaskID: "t4", Status: "assigned", CRMContactID: "6f1c2d3e-0000-4000-8000-000000000001",
		CustomerEmail: "amina@example.com", CustomerName: "Amina Wanjiru", CustomerPhone: "0712345678",
	}.toMap()
	if linked["crm_contact_id"] != "6f1c2d3e-0000-4000-8000-000000000001" {
		t.Fatalf("crm_contact_id missing: %v", linked)
	}
	for _, k := range []string{"customer_email", "customer_name", "customer_phone"} {
		if _, ok := linked[k]; ok {
			t.Fatalf("%s must not ride with a crm_contact_id: %v", k, linked)
		}
	}

	// Nothing known, nothing sent.
	bare := TaskEventData{TaskID: "t5", Status: "en_route_dropoff"}.toMap()
	for _, k := range []string{"crm_contact_id", "customer_email", "customer_name", "customer_phone", "delivered_at", "estimated_arrival"} {
		if _, ok := bare[k]; ok {
			t.Fatalf("empty %s must be omitted: %v", k, bare)
		}
	}
}
