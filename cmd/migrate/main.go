package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/logistics-service/internal/config"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/migrate"
)

func maskPassword(url string) string {
	re := regexp.MustCompile(`://([^:]+):([^@]+)@`)
	return re.ReplaceAllString(url, "://$1:****@")
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	dbURL := cfg.Postgres.URL
	if cfg.Postgres.MigrateURL != "" {
		dbURL = cfg.Postgres.MigrateURL
	}

	log.Printf("connecting to database: %s", maskPassword(dbURL))

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	// Every replica runs this binary on startup — without coordination, N pods launching at
	// once each run their own Schema.Create concurrently against the same tables. This exact
	// unguarded-concurrent-migrate race already caused a production P0 on pos-api
	// (2026-07-26, corrupted a nullable-then-backfill migration on a live table); the same
	// fix has since been applied to pos-api/inventory-api/treasury-api/hospital-api/
	// ordering-backend. A single physical connection (MaxOpenConns=1) + a session-level
	// advisory lock ensures only ONE pod across the whole cluster ever executes the
	// migration at a time; the rest block here until it finishes, then find nothing pending
	// and return immediately.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(5 * time.Minute)

	const migrationLockKey = 727271006 // unique per service — see sibling cmd/migrate/main.go files for the others in use
	if _, err := db.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		log.Fatalf("failed to acquire migration lock: %v", err)
	}
	defer func() {
		if _, err := db.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
			log.Printf("failed to release migration lock: %v", err)
		}
	}()

	drv := entsql.OpenDB(dialect.Postgres, db)
	// Not closed with a defer: the connection is closed after the advisory unlock below (deferred
	// earlier, so it would otherwise run after the close and fail to release).
	client := ent.NewClient(ent.Driver(drv))

	if os.Getenv("RESET_DB") == "true" {
		log.Println("resetting database...")
		if _, err := db.ExecContext(ctx, "DROP SCHEMA IF EXISTS public CASCADE"); err != nil {
			log.Fatalf("failed to drop schema: %v", err)
		}
		if _, err := db.ExecContext(ctx, "CREATE SCHEMA public"); err != nil {
			log.Fatalf("failed to create schema: %v", err)
		}
		log.Println("database reset successful")
	}

	// Ensure Atlas revision tracking is bootstrapped for DBs that were
	// previously managed by Ent auto-migrate (no atlas_schema_revisions table).
	// This is a one-time transition that marks all existing migrations as applied
	// so Atlas only runs genuinely new migrations going forward.
	if err := baselineAtlasIfNeeded(ctx, db); err != nil {
		log.Fatalf("failed to baseline Atlas migrations: %v", err)
	}

	// One-time data cutover that Schema.Create cannot do on its own (it never drops columns or
	// indexes here): make logistics roles platform-wide. Idempotent; runs under the lock above.
	if err := globalizeRoles(ctx, db); err != nil {
		log.Fatalf("failed to make logistics roles platform-wide: %v", err)
	}

	if err := client.Schema.Create(ctx,
		schema.WithDir(migrate.Dir),
	); err != nil {
		log.Fatalf("failed to run migrations: %v", err)
	}

	log.Println("database migrations applied successfully")
}

// baselineAtlasIfNeeded creates atlas_schema_revisions and marks all
// existing migrations (except the newest) as applied, so Atlas only runs
// genuinely new SQL on first deployment after the Ent auto-migrate → Atlas transition.
func baselineAtlasIfNeeded(ctx context.Context, db *sql.DB) error {
	var count int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name = 'atlas_schema_revisions'
	`).Scan(&count)
	if err != nil {
		return fmt.Errorf("checking atlas_schema_revisions: %w", err)
	}
	if count > 0 {
		return nil // Already initialized — normal path for all subsequent deployments
	}

	log.Println("atlas_schema_revisions not found – baselining existing auto-migrated schema")

	if migrate.Dir == nil {
		return fmt.Errorf("migration directory is nil — cannot baseline")
	}

	// Parse atlas.sum to get per-file hashes for correct baseline records
	hashes, err := parseAtlasSum()
	if err != nil {
		log.Printf("warning: could not parse atlas.sum (%v) — hashes will be empty", err)
		hashes = make(map[string]string)
	}

	// Get list of all migration files (sorted chronologically by timestamp prefix)
	files, err := migrate.Dir.Files()
	if err != nil {
		return fmt.Errorf("listing migration files: %w", err)
	}
	if len(files) == 0 {
		return nil
	}

	// Create the Atlas revision tracking table (matches Atlas internal schema)
	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS atlas_schema_revisions (
			version         VARCHAR(255) NOT NULL,
			description     VARCHAR(255) NOT NULL DEFAULT '',
			type            BIGINT       NOT NULL DEFAULT 3,
			applied         INTEGER      NOT NULL DEFAULT 1,
			total           INTEGER      NOT NULL DEFAULT 1,
			executed_at     TIMESTAMPTZ  NOT NULL,
			execution_time  BIGINT       NOT NULL DEFAULT 0,
			error           TEXT,
			error_stmt      TEXT,
			hash            VARCHAR(255) NOT NULL DEFAULT '',
			partial_hashes  TEXT[],
			operator_version VARCHAR(255) NOT NULL DEFAULT '',
			PRIMARY KEY (version)
		)
	`)
	if err != nil {
		return fmt.Errorf("creating atlas_schema_revisions: %w", err)
	}

	// Baseline every migration EXCEPT the newest one.
	// The newest migration (last file chronologically) will be applied by Atlas normally.
	baselineCount := len(files) - 1
	baselineTime := time.Now().UTC().Add(-24 * time.Hour) // mark as applied yesterday

	for i, f := range files {
		if i >= baselineCount {
			break
		}
		version := strings.TrimSuffix(f.Name(), ".sql")
		hash := hashes[f.Name()]

		_, err = db.ExecContext(ctx, `
			INSERT INTO atlas_schema_revisions
				(version, description, type, applied, total, executed_at, hash, operator_version)
			VALUES ($1, $2, 3, 1, 1, $3, $4, 'baseline/v1')
			ON CONFLICT (version) DO NOTHING
		`, version, f.Name(), baselineTime, hash)
		if err != nil {
			return fmt.Errorf("baselining %s: %w", version, err)
		}
		log.Printf("  baselined: %s", version)
	}

	log.Printf("baselined %d existing migrations; newest will be applied by Atlas", baselineCount)
	return nil
}

// parseAtlasSum reads atlas.sum from the embedded migration directory and
// returns a filename → hash map (e.g. "20260314_foo.sql" → "h1:...=").
func parseAtlasSum() (map[string]string, error) {
	f, err := migrate.Dir.Open("atlas.sum")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	hashes := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "h1:") {
			continue // skip overall checksum line and blank lines
		}
		parts := strings.Fields(line)
		if len(parts) == 2 {
			hashes[parts[0]] = parts[1]
		}
	}
	return hashes, nil
}

// globalizeRoles makes logistics roles platform reference data (feedback_shared_core_reference_data):
// the per-tenant copies of each system role become one role, every tenant's assignments and the
// permission links point at it, and the tenant_id column and its indexes go. It also drops the
// never-set user_role_assignments.logistics_role_user_assignments column the old role edge
// created. Rows are backed up to *_bk_global_20261013 first, all in one transaction. A database
// that is already global (no logistics_roles.tenant_id) is left alone.
func globalizeRoles(ctx context.Context, db *sql.DB) error {
	var pending bool
	if err := db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'logistics_roles' AND column_name = 'tenant_id')
	`).Scan(&pending); err != nil {
		return fmt.Errorf("check logistics_roles.tenant_id: %w", err)
	}
	if !pending {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	steps := []struct{ name, sql string }{
		{"backup roles", `CREATE TABLE IF NOT EXISTS logistics_roles_bk_global_20261013 AS SELECT * FROM logistics_roles`},
		{"backup role permissions", `CREATE TABLE IF NOT EXISTS role_permissions_bk_global_20261013 AS SELECT * FROM role_permissions`},
		{"backup assignments", `CREATE TABLE IF NOT EXISTS user_role_assignments_bk_global_20261013 AS SELECT * FROM user_role_assignments`},
		// The oldest row of each role code is kept; every other copy maps to it.
		{"map duplicates", `CREATE TEMP TABLE role_map ON COMMIT DROP AS
			SELECT r.id AS old_id, c.id AS new_id
			FROM logistics_roles r
			JOIN (SELECT DISTINCT ON (role_code) role_code, id FROM logistics_roles
			      ORDER BY role_code, created_at, id) c USING (role_code)
			WHERE r.id <> c.id`},
		// An assignment that would duplicate one the user already has in that tenant goes.
		{"drop duplicate assignments", `DELETE FROM user_role_assignments a USING role_map m
			WHERE a.role_id = m.old_id AND EXISTS (
				SELECT 1 FROM user_role_assignments b
				WHERE b.tenant_id = a.tenant_id AND b.user_id = a.user_id AND b.role_id = m.new_id)`},
		{"repoint assignments", `UPDATE user_role_assignments a SET role_id = m.new_id
			FROM role_map m WHERE a.role_id = m.old_id`},
		// The kept role's permission set is reset from rbac.SystemRoles by the seed after this.
		{"drop duplicate permission links", `DELETE FROM role_permissions p USING role_map m WHERE p.role_id = m.old_id`},
		{"drop duplicate roles", `DELETE FROM logistics_roles r USING role_map m WHERE r.id = m.old_id`},
		{"drop tenant role indexes", `DROP INDEX IF EXISTS logisticsrole_tenant_id_role_code; DROP INDEX IF EXISTS logisticsrole_tenant_id`},
		{"drop roles.tenant_id", `ALTER TABLE logistics_roles DROP COLUMN tenant_id`},
		{"drop stray assignment column", `ALTER TABLE user_role_assignments DROP COLUMN IF EXISTS logistics_role_user_assignments`},
	}
	for _, st := range steps {
		res, err := tx.ExecContext(ctx, st.sql)
		if err != nil {
			return fmt.Errorf("%s: %w", st.name, err)
		}
		n, _ := res.RowsAffected()
		log.Printf("  roles cutover: %s (%d rows)", st.name, n)
	}

	var roles, dupCodes int
	if err := tx.QueryRowContext(ctx, `SELECT count(*), count(*) - count(DISTINCT role_code) FROM logistics_roles`).Scan(&roles, &dupCodes); err != nil {
		return fmt.Errorf("verify roles: %w", err)
	}
	if dupCodes != 0 {
		return fmt.Errorf("verify roles: %d duplicate role codes remain", dupCodes)
	}
	var orphans int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_role_assignments a
		LEFT JOIN logistics_roles r ON r.id = a.role_id WHERE r.id IS NULL`).Scan(&orphans); err != nil {
		return fmt.Errorf("verify assignments: %w", err)
	}
	if orphans != 0 {
		return fmt.Errorf("verify assignments: %d point at no role", orphans)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("logistics roles are platform-wide: %d roles", roles)
	return nil
}
