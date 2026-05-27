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
	db.SetMaxIdleConns(cfg.Postgres.MaxIdleConns)
	db.SetMaxOpenConns(cfg.Postgres.MaxOpenConns)
	db.SetConnMaxLifetime(cfg.Postgres.ConnMaxLifetime)

	drv := entsql.OpenDB(dialect.Postgres, db)
	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()

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
