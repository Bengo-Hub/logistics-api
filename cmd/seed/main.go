package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/logistics-service/internal/config"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/logisticspermission"
	"github.com/bengobox/logistics-service/internal/ent/ratelimitconfig"
	"github.com/bengobox/logistics-service/internal/ent/serviceconfig"
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
	if err := seedPermissions(ctx, client); err != nil {
		return fmt.Errorf("seed permissions: %w", err)
	}
	if err := seedRateLimitConfigs(ctx, client); err != nil {
		return fmt.Errorf("seed rate limit configs: %w", err)
	}
	if err := seedServiceConfigs(ctx, client); err != nil {
		return fmt.Errorf("seed service configs: %w", err)
	}
	return nil
}

// seedPermissions seeds all logistics permissions (idempotent via upsert).
func seedPermissions(ctx context.Context, client *ent.Client) error {
	modules := []string{
		"tasks", "fleet", "vehicles", "zones", "geofences",
		"carriers", "routing", "telemetry", "earnings", "config", "users",
	}
	actions := []string{
		"add", "view", "view_own", "change", "change_own",
		"delete", "delete_own", "manage", "manage_own",
	}

	count := 0
	for _, mod := range modules {
		for _, action := range actions {
			code := fmt.Sprintf("logistics.%s.%s", mod, action)
			name := fmt.Sprintf("%s %s", strings.Title(mod), strings.ReplaceAll(action, "_", " "))

			exists, err := client.LogisticsPermission.Query().
				Where(logisticspermission.PermissionCode(code)).
				Exist(ctx)
			if err != nil {
				return fmt.Errorf("check permission %s: %w", code, err)
			}
			if exists {
				continue
			}

			_, err = client.LogisticsPermission.Create().
				SetPermissionCode(code).
				SetName(name).
				SetModule(mod).
				SetAction(action).
				SetResource(mod).
				Save(ctx)
			if err != nil {
				return fmt.Errorf("create permission %s: %w", code, err)
			}
			count++
		}
	}

	log.Printf("seeded %d new permissions (skipped existing)", count)
	return nil
}

// seedRateLimitConfigs seeds default rate limit configurations.
func seedRateLimitConfigs(ctx context.Context, client *ent.Client) error {
	type rlConfig struct {
		ServiceName      string
		KeyType          string
		EndpointPattern  string
		RequestsPerWin   int
		WindowSec        int
		BurstMultiplier  float64
		Description      string
	}

	configs := []rlConfig{
		{"logistics-api", "tenant", "*", 120, 60, 1.5, "Default tenant rate limit"},
		{"logistics-api", "ip", "*", 60, 60, 2.0, "Default IP rate limit"},
		{"logistics-api", "user", "*", 90, 60, 1.5, "Default per-user rate limit"},
		{"logistics-api", "endpoint", "/api/v1/*/tasks", 200, 60, 2.0, "Task endpoints rate limit"},
		{"logistics-api", "endpoint", "/api/v1/*/routing/*", 100, 60, 1.5, "Routing endpoints rate limit"},
		{"logistics-api", "endpoint", "/api/v1/*/tracking/*", 300, 60, 2.0, "Tracking endpoints rate limit"},
		{"logistics-api", "endpoint", "/api/v1/*/fleet/*", 150, 60, 1.5, "Fleet endpoints rate limit"},
	}

	count := 0
	for _, c := range configs {
		exists, err := client.RateLimitConfig.Query().
			Where(
				ratelimitconfig.ServiceName(c.ServiceName),
				ratelimitconfig.KeyType(c.KeyType),
				ratelimitconfig.EndpointPattern(c.EndpointPattern),
			).Exist(ctx)
		if err != nil {
			return fmt.Errorf("check rate limit config: %w", err)
		}
		if exists {
			continue
		}

		_, err = client.RateLimitConfig.Create().
			SetServiceName(c.ServiceName).
			SetKeyType(c.KeyType).
			SetEndpointPattern(c.EndpointPattern).
			SetRequestsPerWindow(c.RequestsPerWin).
			SetWindowSeconds(c.WindowSec).
			SetBurstMultiplier(c.BurstMultiplier).
			SetDescription(c.Description).
			SetIsActive(true).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create rate limit config: %w", err)
		}
		count++
	}

	log.Printf("seeded %d new rate limit configs (skipped existing)", count)
	return nil
}

// seedServiceConfigs seeds default service configuration entries.
func seedServiceConfigs(ctx context.Context, client *ent.Client) error {
	type svcConfig struct {
		Key         string
		Value       string
		Type        string
		Description string
		IsSecret    bool
	}

	configs := []svcConfig{
		{"logistics.default_task_timeout", "3600", "int", "Default task timeout in seconds", false},
		{"logistics.max_concurrent_tasks", "50", "int", "Maximum concurrent tasks per rider", false},
		{"logistics.max_fleet_size", "500", "int", "Maximum fleet members per tenant", false},
		{"logistics.auto_assign_enabled", "true", "bool", "Whether auto-assignment of tasks is enabled", false},
		{"logistics.geofence_radius_meters", "500", "int", "Default geofence radius in meters", false},
		{"logistics.telemetry_interval_seconds", "10", "int", "Telemetry reporting interval in seconds", false},
		{"logistics.pod_required", "true", "bool", "Whether proof of delivery is required", false},
		{"logistics.max_route_waypoints", "25", "int", "Maximum waypoints per routing request", false},
		{"logistics.earnings_payout_cycle_days", "7", "int", "Earnings payout cycle in days", false},
		{"logistics.tracking_link_expiry_hours", "48", "int", "Public tracking link expiry in hours", false},
	}

	count := 0
	for _, c := range configs {
		exists, err := client.ServiceConfig.Query().
			Where(
				serviceconfig.ConfigKey(c.Key),
				serviceconfig.TenantIDIsNil(),
			).Exist(ctx)
		if err != nil {
			return fmt.Errorf("check service config %s: %w", c.Key, err)
		}
		if exists {
			continue
		}

		_, err = client.ServiceConfig.Create().
			SetConfigKey(c.Key).
			SetConfigValue(c.Value).
			SetConfigType(c.Type).
			SetDescription(c.Description).
			SetIsSecret(c.IsSecret).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create service config %s: %w", c.Key, err)
		}
		count++
	}

	log.Printf("seeded %d new service configs (skipped existing)", count)
	return nil
}
