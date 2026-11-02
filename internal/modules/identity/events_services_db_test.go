package identity

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/user"
)

// auth-api's services list decides whether logistics keeps a member: on auth.user.updated a list
// without logistics removes the member's copy when it belongs to their tenant; an event without
// the list updates as before. Runs on a throwaway schema of the local logistics database.
func TestUserUpdatedServicesListRemovesLogisticsCopy(t *testing.T) {
	url := os.Getenv("LOGISTICS_TEST_DATABASE_URL")
	if url == "" {
		url = "postgres://postgres:postgres@localhost:5432/logistics?sslmode=disable"
	}
	admin, err := sql.Open("pgx", url)
	if err != nil || admin.Ping() != nil {
		t.Skip("local postgres unavailable")
	}
	schema := "lg_user_services_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec("DROP SCHEMA " + schema + " CASCADE"); _ = admin.Close() })
	db, err := sql.Open("pgx", url+"&search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("schema: %v", err)
	}

	h := &EventHandler{service: &Service{client: client}}
	now := time.Now()
	tenantID := uuid.New()
	client.Tenant.Create().SetID(tenantID).SetName("Shop").SetSlug("shop").SetStatus("active").
		SetSyncStatus("synced").SetCreatedAt(now).SetUpdatedAt(now).ExecX(ctx)
	seed := func() uuid.UUID {
		id := uuid.New()
		client.User.Create().SetID(id).SetAuthServiceUserID(id).SetTenantID(tenantID).SetEmail(id.String() + "@shop.test").
			SetFullName("Staff").SetStatus("active").SetSyncStatus("synced").SetCreatedAt(now).SetUpdatedAt(now).ExecX(ctx)
		return id
	}
	updated := func(id uuid.UUID, payload map[string]any) error {
		payload["user_id"] = id.String()
		return h.handleUserUpdated(ctx, sharedevents.NewEvent("updated", "auth.user", id, tenantID, payload))
	}
	exists := func(id uuid.UUID) bool { return client.User.Query().Where(user.AuthServiceUserID(id)).ExistX(ctx) }

	cashier := seed()
	if err := updated(cashier, map[string]any{"services": []any{"pos", "ordering"}}); err != nil {
		t.Fatal(err)
	}
	if exists(cashier) {
		t.Fatal("a member whose services leave out logistics must be removed")
	}

	rider := seed()
	if err := updated(rider, map[string]any{"services": []any{"logistics"}, "full_name": "Rider One"}); err != nil {
		t.Fatal(err)
	}
	if u := client.User.Query().Where(user.AuthServiceUserID(rider)).OnlyX(ctx); u.FullName != "Rider One" {
		t.Fatalf("a logistics member must be updated, got %q", u.FullName)
	}
	if err := updated(rider, map[string]any{"full_name": "Rider Two"}); err != nil || !exists(rider) {
		t.Fatalf("an event without services must update as before: err %v", err)
	}
}
