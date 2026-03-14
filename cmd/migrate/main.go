package main

import (
	"context"
	"database/sql"
	"log"
	"os"
	"regexp"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect/sql/schema"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/logistics-service/internal/config"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/migrate"
)

// maskPassword masks the password in a database URL for logging
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

	log.Printf("connecting to database: %s", maskPassword(cfg.Postgres.URL))

	db, err := sql.Open("pgx", cfg.Postgres.URL)
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

	if err := client.Schema.Create(ctx, 
		schema.WithDir(migrate.Dir),
	); err != nil {
		log.Fatalf("failed to run migrations: %v", err)
	}

	log.Println("database migrations applied successfully")
}
