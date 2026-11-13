package tasks

import (
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent/task"
)

// The CRM link check and the CRM contact consumer find a contact's tasks by comparing the
// lower-cased metadata id with the canonical lower-case id, passed as a numbered placeholder.
func TestMetadataContactSQL(t *testing.T) {
	id := uuid.New()
	sel := sql.Dialect(dialect.Postgres).Select("*").From(sql.Table(task.Table))
	MetadataContact(id)(sel)
	query, args := sel.Query()
	if !strings.Contains(query, `lower("tasks"."metadata" ->> 'crm_contact_id') = $1`) {
		t.Fatalf("unexpected predicate: %s", query)
	}
	if len(args) != 1 || args[0] != id.String() {
		t.Fatalf("args = %v, want the canonical id", args)
	}
}
