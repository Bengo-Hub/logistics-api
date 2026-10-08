// Command prune-users removes the users logistics took in before auth.user events were gated by
// shared UserRelevance (2026-10-08). Back then every member of every tenant was created here and
// resolveRole made anyone unrecognised a driver. A row is removed only when nothing in logistics
// depends on it: role driver, not an invited stub, no fleet membership and no tasks. Real riders
// always have a fleet membership. Anyone removed by mistake comes back on first sign-in.
//
// It reports counts per tenant and changes nothing unless run with --apply.
package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"sort"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/logistics-service/internal/config"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/tenant"
	"github.com/bengobox/logistics-service/internal/ent/user"
	"github.com/bengobox/logistics-service/internal/ent/userroleassignment"
)

func main() {
	apply := flag.Bool("apply", false, "delete the rows (default reports only)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	dbURL := cfg.Postgres.URL
	if cfg.Postgres.MigrateURL != "" {
		dbURL = cfg.Postgres.MigrateURL
	}
	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer client.Close()

	total, err := client.User.Query().Count(ctx)
	if err != nil {
		log.Fatalf("count users: %v", err)
	}
	stale, err := client.User.Query().
		Where(
			user.Role("driver"),
			user.StatusNEQ("invited"),
			user.Not(user.HasFleetMemberships()),
			user.Not(user.HasTasks()),
		).
		Select(user.FieldID, user.FieldTenantID).
		All(ctx)
	if err != nil {
		log.Fatalf("find stale users: %v", err)
	}

	byTenant := map[uuid.UUID]int{}
	ids := make([]uuid.UUID, 0, len(stale))
	for _, u := range stale {
		byTenant[u.TenantID]++
		ids = append(ids, u.ID)
	}
	slugs := map[uuid.UUID]string{}
	if len(byTenant) > 0 {
		tids := make([]uuid.UUID, 0, len(byTenant))
		for id := range byTenant {
			tids = append(tids, id)
		}
		ts, err := client.Tenant.Query().Where(tenant.IDIn(tids...)).All(ctx)
		if err != nil {
			log.Fatalf("load tenants: %v", err)
		}
		for _, t := range ts {
			slugs[t.ID] = t.Slug
		}
	}
	keys := make([]uuid.UUID, 0, len(byTenant))
	for id := range byTenant {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool { return byTenant[keys[i]] > byTenant[keys[j]] })
	for _, id := range keys {
		log.Printf("tenant %-28s %s  removable: %d", slugs[id], id, byTenant[id])
	}
	log.Printf("users: %d total, %d removable, %d kept", total, len(ids), total-len(ids))

	if !*apply || len(ids) == 0 {
		log.Printf("dry run: nothing changed (pass --apply to delete)")
		return
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		log.Fatalf("begin: %v", err)
	}
	roles, err := tx.UserRoleAssignment.Delete().Where(userroleassignment.UserIDIn(ids...)).Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		log.Fatalf("delete role assignments: %v", err)
	}
	users, err := tx.User.Delete().Where(user.IDIn(ids...)).Exec(ctx)
	if err != nil {
		_ = tx.Rollback()
		log.Fatalf("delete users: %v", err)
	}
	if err := tx.Commit(); err != nil {
		log.Fatalf("commit: %v", err)
	}
	log.Printf("deleted %d users and %d role assignments", users, roles)
}
