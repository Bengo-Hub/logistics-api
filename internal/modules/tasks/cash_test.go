package tasks

import (
	"testing"

	"github.com/bengobox/logistics-service/internal/ent"
)

func TestCashAmount(t *testing.T) {
	cases := []struct {
		name      string
		collected float64
		task      *ent.Task
		want      float64
	}{
		// Customer paid 1000 for an 850 order and got change: the rider owes 850.
		{"change given", 1000, &ent.Task{CashOnDelivery: 850}, 850},
		{"exact", 850, &ent.Task{CashOnDelivery: 850}, 850},
		{"cod in metadata", 500, &ent.Task{Metadata: map[string]any{"cash_on_delivery": 450.0}}, 450},
		{"no cod on task", 300, &ent.Task{}, 300},
		{"task missing", 300, nil, 300},
	}
	for _, tc := range cases {
		got := cashAmount(&ent.ProofOfDelivery{AmountCollected: tc.collected}, tc.task)
		if got != tc.want {
			t.Errorf("%s: cashAmount = %v, want %v", tc.name, got, tc.want)
		}
	}
}
