package handlers

import (
	"net/http/httptest"
	"testing"

	"github.com/Bengo-Hub/httpware"
	"github.com/google/uuid"
)

func TestOutletFilterOnlyAddsAClauseForADrillDown(t *testing.T) {
	tenant := uuid.New()
	clause, args := outletFilter(uuid.Nil, []any{tenant, "since"})
	if clause != "" || len(args) != 2 {
		t.Fatalf("all outlets must leave the query untouched, got %q %v", clause, args)
	}
	outlet := uuid.New()
	clause, args = outletFilter(outlet, []any{tenant, "since", "day", "Africa/Nairobi"})
	if clause != " AND t.outlet_id = $5" || len(args) != 5 || args[4] != outlet {
		t.Fatalf("the outlet parameter must follow the existing ones, got %q %v", clause, args)
	}
}

func TestAnalyticsOutletReadsTheOutletHeaderContext(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/urban-loft/analytics/kpis", nil)
	if got := analyticsOutlet(r); got != uuid.Nil {
		t.Fatalf("no header means every outlet, got %s", got)
	}
	id := uuid.New()
	r = r.WithContext(httpware.WithOutletID(r.Context(), id.String()))
	if got := analyticsOutlet(r); got != id {
		t.Fatalf("outlet = %s, want %s", got, id)
	}
	r = r.WithContext(httpware.WithOutletID(r.Context(), "not-a-uuid"))
	if got := analyticsOutlet(r); got != uuid.Nil {
		t.Fatalf("a garbled header must not scope or fail, got %s", got)
	}
}

func TestZoneRequestsAreScopedToTheExplicitOutletThenTheSwitcher(t *testing.T) {
	header, explicit := uuid.New(), uuid.New()
	r := httptest.NewRequest("GET", "/api/v1/urban-loft/zones?outlet_id="+explicit.String(), nil)
	r = r.WithContext(httpware.WithOutletID(r.Context(), header.String()))
	if got := outletParam(r); got != explicit {
		t.Fatalf("an explicit outlet_id wins, got %s", got)
	}
	r = httptest.NewRequest("GET", "/api/v1/urban-loft/zones", nil)
	r = r.WithContext(httpware.WithOutletID(r.Context(), header.String()))
	if got := outletParam(r); got != header {
		t.Fatalf("the switched outlet applies without a parameter, got %s", got)
	}
	if got := outletParam(httptest.NewRequest("GET", "/api/v1/urban-loft/zones", nil)); got != uuid.Nil {
		t.Fatalf("no outlet means every outlet, got %s", got)
	}
}
