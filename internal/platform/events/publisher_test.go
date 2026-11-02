package events

import "testing"

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
