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

// Employment types. Freelance riders are paid per delivery and go through KYC review. Staff
// (internal) riders are employees HR has already vetted: the admin onboards them directly,
// KYC uploads are optional once their ID number is captured, and a rider's pay basis says how
// they are paid.
const (
	EmploymentFreelance = "freelance"
	EmploymentStaff     = "staff"
)

// Pay bases. Payroll riders draw a salary on erp-api payroll and earn nothing per delivery
// here (their per diem and allowances are erp-api expense claims; logistics only reports the
// trip). Earning-rules riders are paid per delivery from the rider pricing rules, exactly like
// freelance riders.
const (
	PayBasisPayroll      = "payroll"
	PayBasisEarningRules = "earning_rules"
)

// metaLegacyPerTask is the per_task_earnings override that PayBasis replaced. It is still read
// so riders saved with it keep their pay, and never written.
const metaLegacyPerTask = "per_task_earnings"

// Employment describes how a rider is engaged and paid. It lives in fleet_members.metadata so
// new terms need no migration. The staff number and salary stay on the erp-api employee, found
// by the rider's auth user id.
type Employment struct {
	Type     string `json:"type"`
	PayBasis string `json:"pay_basis,omitempty"`
}

// Normalize validates the employment terms and fills the defaults: freelance riders are
// always paid by earning rules, staff riders by payroll unless set otherwise.
func (e Employment) Normalize() (Employment, error) {
	e.Type = strings.ToLower(strings.TrimSpace(e.Type))
	if e.Type == "" {
		e.Type = EmploymentFreelance
	}
	if e.Type != EmploymentFreelance && e.Type != EmploymentStaff {
		return e, fmt.Errorf("fleet: employment type must be %q or %q", EmploymentFreelance, EmploymentStaff)
	}
	e.PayBasis = strings.ToLower(strings.TrimSpace(e.PayBasis))
	switch {
	case e.Type == EmploymentFreelance:
		e.PayBasis = PayBasisEarningRules
	case e.PayBasis == "":
		e.PayBasis = PayBasisPayroll
	case e.PayBasis != PayBasisPayroll && e.PayBasis != PayBasisEarningRules:
		return e, fmt.Errorf("fleet: pay basis must be %q or %q", PayBasisPayroll, PayBasisEarningRules)
	}
	return e, nil
}

// ToMap renders the terms for the metadata column.
func (e Employment) ToMap() map[string]any {
	return map[string]any{"type": e.Type, "pay_basis": e.PayBasis}
}

// EmploymentOf reads a member's employment terms from metadata. Members without terms are
// freelance on earning rules, which is how every rider was treated before staff riders existed.
func EmploymentOf(m *ent.FleetMember) Employment {
	e := Employment{Type: EmploymentFreelance, PayBasis: PayBasisEarningRules}
	if m == nil || m.Metadata == nil {
		return e
	}
	raw, ok := m.Metadata[MetaEmployment].(map[string]any)
	if !ok {
		return e
	}
	if t, ok := raw["type"].(string); ok && t == EmploymentStaff {
		e.Type = EmploymentStaff
		e.PayBasis = PayBasisPayroll
		if pb, ok := raw["pay_basis"].(string); ok && pb == PayBasisEarningRules {
			e.PayBasis = PayBasisEarningRules
		} else if v, ok := raw[metaLegacyPerTask].(bool); ok && v {
			e.PayBasis = PayBasisEarningRules
		}
	}
	return e
}

// EarnsPerTask reports whether the rider is paid per delivery from the pricing rules.
func (e Employment) EarnsPerTask() bool { return e.PayBasis != PayBasisPayroll }

// IsStaff reports whether the rider is an internal employee (vetted by HR, no KYC review).
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
	upd := m.Update().SetMetadata(meta)
	// Staff riders need no KYC review: a rider still waiting on it is activated.
	activate := emp.IsStaff() && NeedsKYC(m.Status)
	if activate {
		upd.SetStatus("active")
	}
	saved, err := upd.Save(ctx)
	if err != nil {
		return nil, err
	}
	if activate {
		s.ensureDriverRole(ctx, tenantID, m.UserID)
		s.statusChanged(ctx, tenantID)
	}
	return saved, nil
}

// NeedsKYC reports whether a member status is still waiting on KYC documents or review.
func NeedsKYC(status string) bool {
	return status == "pending" || status == "pending_review"
}
