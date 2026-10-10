package earnings

import "testing"

func f(v float64) *float64 { return &v }

func TestValidateRule(t *testing.T) {
	ok := RuleInput{Name: "Standard", RuleType: "distance", BaseFee: 50, PerKmRate: f(20), IsActive: true,
		DistanceTiers: []map[string]any{{"max_km": 5.0, "rate": 30.0}, {"max_km": 10.0, "rate": 20.0}}}
	if err := ValidateRule(ok); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	bad := []RuleInput{
		{Name: "", RuleType: "flat"},
		{Name: "x", RuleType: "teleport"},
		{Name: "x", RuleType: "flat", BaseFee: -1},
		{Name: "x", RuleType: "distance", PerKmRate: f(-5)},
		{Name: "x", RuleType: "surge", SurgeMultiplier: f(0)},
		{Name: "x", RuleType: "distance", DistanceTiers: []map[string]any{{"max_km": 0.0, "rate": 10.0}}},
		{Name: "x", RuleType: "distance", DistanceTiers: []map[string]any{{"max_km": 5.0}}},
	}
	for i, b := range bad {
		if err := ValidateRule(b); err == nil {
			t.Errorf("case %d should be rejected", i)
		}
	}
}
