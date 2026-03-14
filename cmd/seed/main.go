package main

import (
	"context"
	"log"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"database/sql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/logistics-service/internal/config"
	"github.com/bengobox/logistics-service/internal/ent"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	db, err := sql.Open("pgx", cfg.Postgres.URL)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	driver := entsql.OpenDB(dialect.Postgres, db)

	client := ent.NewClient(ent.Driver(driver))
	defer client.Close()

	if err := runSeed(ctx, client); err != nil {
		log.Fatalf("failed to seed data: %v", err)
	}

	log.Println("database seed completed successfully")
}

func runSeed(ctx context.Context, client *ent.Client) error {
	// Add your seeding logic here (idempotent)
	log.Println("no seeding logic defined yet for logistics-api")
	return nil
}
