package fleet

import (
	"strings"
	"testing"

	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
)

// The rider outlet filter renders a Postgres placeholder for the outlet, never a raw '?'.
func TestServesOutletSQL(t *testing.T) {
	outlet := uuid.New()
	sel := entsql.Dialect("postgres").Select("*").From(entsql.Table(fleetmember.Table))
	ServesOutlet(outlet)(sel)
	query, args := sel.Query()
	if strings.Contains(query, "?") || !strings.Contains(query, "$1") {
		t.Fatalf("placeholder not rendered for postgres: %s", query)
	}
	if len(args) != 1 || args[0] != outlet.String() {
		t.Fatalf("args = %v", args)
	}
}
