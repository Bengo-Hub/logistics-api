package earnings

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/pricingrule"
)

// ErrRuleNotFound means the rule does not exist for this tenant.
var ErrRuleNotFound = errors.New("pricing rule not found")

// RuleInput creates a rider pricing rule. Rider pricing rules decide what a rider earns
// for a delivery that carries no delivery fee; customer delivery prices live in the
// zones delivery policy.
type RuleInput struct {
	Name            string           `json:"name"`
	RuleType        string           `json:"rule_type"`
	BaseFee         float64          `json:"base_fee"`
	PerKmRate       *float64         `json:"per_km_rate,omitempty"`
	SurgeMultiplier *float64         `json:"surge_multiplier,omitempty"`
	IsActive        bool             `json:"is_active"`
	Priority        int              `json:"priority"`
	DistanceTiers   []map[string]any `json:"distance_tiers,omitempty"`
}

// RulePatch updates selected fields of a rule.
type RulePatch struct {
	Name            *string          `json:"name,omitempty"`
	RuleType        *string          `json:"rule_type,omitempty"`
	BaseFee         *float64         `json:"base_fee,omitempty"`
	PerKmRate       *float64         `json:"per_km_rate,omitempty"`
	SurgeMultiplier *float64         `json:"surge_multiplier,omitempty"`
	IsActive        *bool            `json:"is_active,omitempty"`
	Priority        *int             `json:"priority,omitempty"`
	DistanceTiers   []map[string]any `json:"distance_tiers,omitempty"`
}

// ValidateRule checks a full rule definition. Pure, for tests.
func ValidateRule(in RuleInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if err := pricingrule.RuleTypeValidator(pricingrule.RuleType(in.RuleType)); err != nil {
		return fmt.Errorf("rule_type must be one of distance, weight, time_of_day, surge, flat")
	}
	if in.BaseFee < 0 || (in.PerKmRate != nil && *in.PerKmRate < 0) {
		return fmt.Errorf("fees cannot be negative")
	}
	if in.SurgeMultiplier != nil && *in.SurgeMultiplier <= 0 {
		return fmt.Errorf("surge_multiplier must be above 0")
	}
	return validateTiers(in.DistanceTiers)
}

// validateTiers requires each tier to have a positive max_km and a non-negative rate.
func validateTiers(tiers []map[string]any) error {
	for i, t := range tiers {
		maxKm, ok1 := t["max_km"].(float64)
		rate, ok2 := t["rate"].(float64)
		if !ok1 || !ok2 || maxKm <= 0 || rate < 0 {
			return fmt.Errorf("distance tier %d needs max_km above 0 and a rate of 0 or more", i+1)
		}
	}
	return nil
}

// ListRules returns the tenant's rider pricing rules, highest priority first.
func (s *Service) ListRules(ctx context.Context, tenantID uuid.UUID) ([]*ent.PricingRule, error) {
	return s.client.PricingRule.Query().
		Where(pricingrule.TenantID(tenantID)).
		Order(ent.Desc(pricingrule.FieldPriority), ent.Asc(pricingrule.FieldName)).
		All(ctx)
}

// CreateRule validates and stores a rule.
func (s *Service) CreateRule(ctx context.Context, tenantID uuid.UUID, in RuleInput) (*ent.PricingRule, error) {
	if err := ValidateRule(in); err != nil {
		return nil, err
	}
	c := s.client.PricingRule.Create().
		SetTenantID(tenantID).
		SetName(strings.TrimSpace(in.Name)).
		SetRuleType(pricingrule.RuleType(in.RuleType)).
		SetBaseFee(in.BaseFee).
		SetIsActive(in.IsActive).
		SetPriority(in.Priority).
		SetNillablePerKmRate(in.PerKmRate).
		SetNillableSurgeMultiplier(in.SurgeMultiplier)
	if len(in.DistanceTiers) > 0 {
		c = c.SetDistanceTiers(in.DistanceTiers)
	}
	return c.Save(ctx)
}

// UpdateRule applies a patch after re-validating the resulting rule.
func (s *Service) UpdateRule(ctx context.Context, tenantID, ruleID uuid.UUID, p RulePatch) (*ent.PricingRule, error) {
	rule, err := s.client.PricingRule.Query().Where(pricingrule.ID(ruleID), pricingrule.TenantID(tenantID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrRuleNotFound
		}
		return nil, err
	}
	next := RuleInput{
		Name: rule.Name, RuleType: string(rule.RuleType), BaseFee: rule.BaseFee, PerKmRate: rule.PerKmRate,
		SurgeMultiplier: rule.SurgeMultiplier, IsActive: rule.IsActive, Priority: rule.Priority, DistanceTiers: rule.DistanceTiers,
	}
	if p.Name != nil {
		next.Name = *p.Name
	}
	if p.RuleType != nil {
		next.RuleType = *p.RuleType
	}
	if p.BaseFee != nil {
		next.BaseFee = *p.BaseFee
	}
	if p.PerKmRate != nil {
		next.PerKmRate = p.PerKmRate
	}
	if p.SurgeMultiplier != nil {
		next.SurgeMultiplier = p.SurgeMultiplier
	}
	if p.IsActive != nil {
		next.IsActive = *p.IsActive
	}
	if p.Priority != nil {
		next.Priority = *p.Priority
	}
	if p.DistanceTiers != nil {
		next.DistanceTiers = p.DistanceTiers
	}
	if err := ValidateRule(next); err != nil {
		return nil, err
	}
	u := rule.Update().
		SetName(strings.TrimSpace(next.Name)).
		SetRuleType(pricingrule.RuleType(next.RuleType)).
		SetBaseFee(next.BaseFee).
		SetIsActive(next.IsActive).
		SetPriority(next.Priority).
		SetNillablePerKmRate(next.PerKmRate).
		SetNillableSurgeMultiplier(next.SurgeMultiplier)
	if next.DistanceTiers != nil {
		u = u.SetDistanceTiers(next.DistanceTiers)
	}
	return u.Save(ctx)
}

// DeleteRule removes a rule of this tenant.
func (s *Service) DeleteRule(ctx context.Context, tenantID, ruleID uuid.UUID) error {
	n, err := s.client.PricingRule.Delete().Where(pricingrule.ID(ruleID), pricingrule.TenantID(tenantID)).Exec(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrRuleNotFound
	}
	return nil
}
