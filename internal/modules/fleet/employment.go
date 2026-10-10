package fleet

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
)

// MetaEmployment is the fleet member metadata key holding the rider's employment terms.
const MetaEmployment = "employment"

// Employment types. Freelance riders are paid per delivery. Staff riders are employees on
// erp-api payroll: they earn nothing per delivery here unless overridden, and their per diem
// and allowances are erp-api expense claims (logistics only reports the trip).
const (
	EmploymentFreelance = "freelance"
	EmploymentStaff     = "staff"
)

// Employment describes how a rider is engaged. It lives in fleet_members.metadata so new
// terms need no migration. The staff number and salary stay on the erp-api employee, found
// by the rider's auth user id.
type Employment struct {
	Type string `json:"type"`
	// PerTaskEarnings overrides the default (freelance earn per task, staff do not).
	PerTaskEarnings *bool `json:"per_task_earnings,omitempty"`
}

// Normalize validates the employment terms and fills the default type.
func (e Employment) Normalize() (Employment, error) {
	e.Type = strings.ToLower(strings.TrimSpace(e.Type))
	if e.Type == "" {
		e.Type = EmploymentFreelance
	}
	if e.Type != EmploymentFreelance && e.Type != EmploymentStaff {
		return e, fmt.Errorf("fleet: employment type must be %q or %q", EmploymentFreelance, EmploymentStaff)
	}
	return e, nil
}

// ToMap renders the terms for the metadata column.
func (e Employment) ToMap() map[string]any {
	m := map[string]any{"type": e.Type}
	if e.PerTaskEarnings != nil {
		m["per_task_earnings"] = *e.PerTaskEarnings
	}
	return m
}

// EmploymentOf reads a member's employment terms from metadata. Members without terms are
// freelance, which is how every rider was treated before staff riders existed.
func EmploymentOf(m *ent.FleetMember) Employment {
	e := Employment{Type: EmploymentFreelance}
	if m == nil || m.Metadata == nil {
		return e
	}
	raw, ok := m.Metadata[MetaEmployment].(map[string]any)
	if !ok {
		return e
	}
	if t, ok := raw["type"].(string); ok && t == EmploymentStaff {
		e.Type = EmploymentStaff
	}
	if v, ok := raw["per_task_earnings"].(bool); ok {
		e.PerTaskEarnings = &v
	}
	return e
}

// EarnsPerTask reports whether the rider is paid per delivery.
func (e Employment) EarnsPerTask() bool {
	if e.PerTaskEarnings != nil {
		return *e.PerTaskEarnings
	}
	return e.Type != EmploymentStaff
}

// IsStaff reports whether the rider is an employee on erp-api payroll.
func (e Employment) IsStaff() bool { return e.Type == EmploymentStaff }

// SetEmployment replaces a member's employment terms, keeping the rest of its metadata.
func (s *Service) SetEmployment(ctx context.Context, tenantID, memberID uuid.UUID, in Employment) (*ent.FleetMember, error) {
	emp, err := in.Normalize()
	if err != nil {
		return nil, err
	}
	m, err := s.client.FleetMember.Query().
		Where(fleetmember.ID(memberID), fleetmember.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	meta := map[string]any{}
	for k, v := range m.Metadata {
		meta[k] = v
	}
	meta[MetaEmployment] = emp.ToMap()
	return m.Update().SetMetadata(meta).Save(ctx)
}
