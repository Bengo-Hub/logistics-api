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
