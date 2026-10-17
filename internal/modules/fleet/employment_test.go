package fleet

import (
	"testing"

	"github.com/bengobox/logistics-service/internal/ent"
)

func TestEmploymentDefaultsAndPayBasis(t *testing.T) {
	if e := EmploymentOf(&ent.FleetMember{}); e.Type != EmploymentFreelance || !e.EarnsPerTask() {
		t.Fatalf("member without terms must be freelance and earn per task, got %+v", e)
	}
	staff := &ent.FleetMember{Metadata: map[string]any{MetaEmployment: map[string]any{"type": "staff"}}}
	if e := EmploymentOf(staff); !e.IsStaff() || e.EarnsPerTask() || e.PayBasis != PayBasisPayroll {
		t.Fatalf("staff must default to payroll and not earn per task, got %+v", e)
	}
	rules := &ent.FleetMember{Metadata: map[string]any{MetaEmployment: map[string]any{"type": "staff", "pay_basis": "earning_rules"}}}
	if e := EmploymentOf(rules); !e.IsStaff() || !e.EarnsPerTask() {
		t.Fatalf("staff on earning rules must earn per task, got %+v", e)
	}
	legacy := &ent.FleetMember{Metadata: map[string]any{MetaEmployment: map[string]any{"type": "staff", "per_task_earnings": true}}}
	if e := EmploymentOf(legacy); !e.EarnsPerTask() || e.PayBasis != PayBasisEarningRules {
		t.Fatalf("the legacy per_task_earnings flag must still read as earning rules, got %+v", e)
	}
}

func TestEmploymentNormalize(t *testing.T) {
	if _, err := (Employment{Type: "contractor"}).Normalize(); err == nil {
		t.Fatal("unknown employment type accepted")
	}
	if _, err := (Employment{Type: "staff", PayBasis: "barter"}).Normalize(); err == nil {
		t.Fatal("unknown pay basis accepted")
	}
	if e, err := (Employment{Type: " Staff "}).Normalize(); err != nil || e.Type != EmploymentStaff || e.PayBasis != PayBasisPayroll {
		t.Fatalf("staff must normalise to payroll: %+v %v", e, err)
	}
	if e, err := (Employment{Type: "staff", PayBasis: " Earning_Rules "}).Normalize(); err != nil || e.PayBasis != PayBasisEarningRules {
		t.Fatalf("pay basis not normalised: %+v %v", e, err)
	}
	if e, err := (Employment{Type: "freelance", PayBasis: "payroll"}).Normalize(); err != nil || e.PayBasis != PayBasisEarningRules {
		t.Fatalf("freelance riders are always on earning rules: %+v %v", e, err)
	}
}
