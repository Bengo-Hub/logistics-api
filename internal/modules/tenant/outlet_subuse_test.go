package tenant

import "testing"

// Food businesses deliver their own orders, shops distribute stock, and outlets that never ship
// goods are not mirrored.
func TestSubUseCaseOf(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"":              {"logistics", true},
		"logistics":     {"logistics", true},
		"courier":       {"courier", true},
		"hospitality":   {"delivery", true},
		"quick_service": {"delivery", true},
		"cafe":          {"delivery", true},
		"retail":        {"distribution", true},
		"pharmacy":      {"distribution", true},
		"services":      {"", false},
		"isp":           {"", false},
		"gift_registry": {"", false},
	}
	for in, want := range cases {
		got, ok := subUseCaseOf(in)
		if got != want.want || ok != want.ok {
			t.Errorf("subUseCaseOf(%q) = %q, %v; want %q, %v", in, got, ok, want.want, want.ok)
		}
	}
}
