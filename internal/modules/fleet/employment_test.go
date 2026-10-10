package fleet

import (
	"testing"

	"github.com/bengobox/logistics-service/internal/ent"
)

func TestEmploymentDefaultsAndOverrides(t *testing.T) {
	if e := EmploymentOf(&ent.FleetMember{}); e.Type != EmploymentFreelance || !e.EarnsPerTask() {
		t.Fatalf("member without terms must be freelance and earn per task, got %+v", e)
	}
	staff := &ent.FleetMember{Metadata: map[string]any{MetaEmployment: map[string]any{"type": "staff"}}}
	if e := EmploymentOf(staff); !e.IsStaff() || e.EarnsPerTask() {
		t.Fatalf("staff must not earn per task by default, got %+v", e)
	}
	paid := &ent.FleetMember{Metadata: map[string]any{MetaEmployment: map[string]any{"type": "staff", "per_task_earnings": true}}}
	if !EmploymentOf(paid).EarnsPerTask() {
		t.Fatal("per_task_earnings=true must override the staff default")
	}
	if _, err := (Employment{Type: "contractor"}).Normalize(); err == nil {
		t.Fatal("unknown employment type accepted")
	}
	if e, err := (Employment{Type: " Staff "}).Normalize(); err != nil || e.Type != EmploymentStaff {
		t.Fatalf("type not normalised: %+v %v", e, err)
	}
}
