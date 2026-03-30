package earnings

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/pricingrule"
)

// distanceTier represents a single tier in the distance-based pricing table.
type distanceTier struct {
	MaxKm float64
	Rate  float64
}

// CalculateDeliveryFee computes the delivery fee for a given distance using the
// tenant's active PricingRules. Rules are evaluated in priority order (highest
// first); the first applicable rule determines the fee. If no rules match, a
// zero fee is returned with an error.
func CalculateDeliveryFee(ctx context.Context, client *ent.Client, tenantID uuid.UUID, distanceKm float64) (float64, error) {
	rules, err := client.PricingRule.Query().
		Where(
			pricingrule.TenantID(tenantID),
			pricingrule.IsActive(true),
		).
		Order(ent.Desc(pricingrule.FieldPriority)).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("earnings: query pricing rules: %w", err)
	}
	if len(rules) == 0 {
		return 0, fmt.Errorf("earnings: no active pricing rules for tenant %s", tenantID)
	}

	// Evaluate rules by priority until one produces a fee.
	for _, rule := range rules {
		fee, ok := applyRule(rule, distanceKm)
		if ok {
			return roundCents(fee), nil
		}
	}

	// Fallback: use the highest-priority rule's base fee.
	return roundCents(rules[0].BaseFee), nil
}

// applyRule attempts to calculate a fee from a single PricingRule.
// Returns the fee and true if the rule was applicable; false otherwise.
func applyRule(rule *ent.PricingRule, distanceKm float64) (float64, bool) {
	var fee float64

	switch rule.RuleType {
	case pricingrule.RuleTypeDistance:
		fee = rule.BaseFee + applyDistanceTiers(rule.DistanceTiers, distanceKm)
		if rule.PerKmRate != nil {
			fee += *rule.PerKmRate * distanceKm
		}
	case pricingrule.RuleTypeFlat:
		fee = rule.BaseFee
	case pricingrule.RuleTypeSurge:
		// Surge rules only contribute a multiplier; they need a base from another rule.
		return 0, false
	case pricingrule.RuleTypeWeight:
		// Weight rules require weight data; skip when calculating by distance only.
		return 0, false
	case pricingrule.RuleTypeTimeOfDay:
		// Time-of-day rules require time window matching; skip for basic distance calc.
		return 0, false
	default:
		return 0, false
	}

	// Apply surge multiplier if present on this rule.
	if rule.SurgeMultiplier != nil && *rule.SurgeMultiplier > 0 {
		fee *= *rule.SurgeMultiplier
	}

	return fee, true
}

// applyDistanceTiers calculates the tiered portion of the delivery fee.
// Tiers are sorted by max_km ascending; each tier covers the range from the
// previous tier's max_km (or 0) up to its own max_km.
func applyDistanceTiers(raw []map[string]interface{}, distanceKm float64) float64 {
	if len(raw) == 0 {
		return 0
	}

	tiers := parseTiers(raw)
	if len(tiers) == 0 {
		return 0
	}

	// Sort tiers ascending by MaxKm.
	sort.Slice(tiers, func(i, j int) bool {
		return tiers[i].MaxKm < tiers[j].MaxKm
	})

	var total float64
	var prevMax float64

	for _, tier := range tiers {
		if distanceKm <= prevMax {
			break
		}
		// Distance covered in this tier band.
		upper := math.Min(distanceKm, tier.MaxKm)
		band := upper - prevMax
		if band > 0 {
			total += band * tier.Rate
		}
		prevMax = tier.MaxKm
	}

	// If distance exceeds all tiers, apply the last tier's rate for the remainder.
	if distanceKm > prevMax && len(tiers) > 0 {
		lastRate := tiers[len(tiers)-1].Rate
		total += (distanceKm - prevMax) * lastRate
	}

	return total
}

// parseTiers converts the raw JSON distance_tiers into typed structs.
func parseTiers(raw []map[string]interface{}) []distanceTier {
	tiers := make([]distanceTier, 0, len(raw))
	for _, m := range raw {
		maxKm, ok1 := toFloat64(m["max_km"])
		rate, ok2 := toFloat64(m["rate"])
		if ok1 && ok2 {
			tiers = append(tiers, distanceTier{MaxKm: maxKm, Rate: rate})
		}
	}
	return tiers
}

// toFloat64 extracts a float64 from a JSON-decoded interface value.
func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// roundCents rounds to 2 decimal places for currency.
func roundCents(v float64) float64 {
	return math.Round(v*100) / 100
}
