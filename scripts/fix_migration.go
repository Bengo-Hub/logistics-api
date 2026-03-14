//go:build ignore

package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dbURL := os.Getenv("POSTGRES_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/logistics?sslmode=disable"
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Drop and recreate public schema for a clean slate
	fmt.Println("Dropping and recreating public schema...")
	_, err = db.Exec("DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO postgres; GRANT ALL ON SCHEMA public TO public;")
	if err != nil {
		log.Fatalf("Error clearing database: %v", err)
	}
	fmt.Println("✓ Successfully cleared database (public schema recreated)")
}
