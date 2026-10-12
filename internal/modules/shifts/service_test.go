package shifts

import "testing"

func TestAllowedTransitions(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
	}{
		{StatusScheduled, StatusActive, true},
		{StatusScheduled, StatusCancelled, true},
		{StatusScheduled, StatusCompleted, false},
		{StatusActive, StatusCompleted, true},
		{StatusActive, StatusCancelled, false},
		{StatusActive, StatusScheduled, false},
		{StatusCompleted, StatusActive, false},
		{StatusCancelled, StatusActive, false},
	}
	for _, c := range cases {
		if got := allowed(c.from, c.to); got != c.ok {
			t.Errorf("allowed(%s, %s) = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
}

func TestValidStatus(t *testing.T) {
	for _, s := range []string{StatusScheduled, StatusActive, StatusCompleted, StatusCancelled} {
		if !validStatus(s) {
			t.Errorf("%s should be valid", s)
		}
	}
	for _, s := range []string{"", "online", "ACTIVE"} {
		if validStatus(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}
