package handlers

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestAnalyticsPeriod(t *testing.T) {
	now := time.Now().In(reportLocation)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, reportLocation)
	cases := []struct {
		query, def, want string
		start            time.Time
	}{
		{"today", "7d", "today", midnight},
		{"7d", "30d", "7d", midnight.AddDate(0, 0, -6)},
		{"30d", "7d", "30d", midnight.AddDate(0, 0, -29)},
		{"90d", "7d", "90d", midnight.AddDate(0, 0, -89)},
		{"", "30d", "30d", midnight.AddDate(0, 0, -29)},
		{"forever", "7d", "7d", midnight.AddDate(0, 0, -6)},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/analytics/kpis?period="+c.query, nil)
		got, start := analyticsPeriod(r, c.def)
		if got != c.want || !start.Equal(c.start) {
			t.Errorf("period %q (default %q) = %s %v, want %s %v", c.query, c.def, got, start, c.want, c.start)
		}
	}
	if reportLocation.String() == "UTC" {
		t.Errorf("reports must count days in local time, got UTC")
	}
}
