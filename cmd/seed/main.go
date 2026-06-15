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
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/bengobox/logistics-service/internal/config"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/logisticspermission"
	"github.com/bengobox/logistics-service/internal/ent/logisticsrole"
	"github.com/bengobox/logistics-service/internal/ent/ratelimitconfig"
	"github.com/bengobox/logistics-service/internal/ent/serviceconfig"
	"github.com/bengobox/logistics-service/internal/ent/user"
	"github.com/bengobox/logistics-service/internal/ent/userroleassignment"
	"github.com/bengobox/logistics-service/internal/modules/tenant"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	// Prefer the direct PostgreSQL URL to bypass PgBouncer for seed DDL/DML.
	dbURL := cfg.Postgres.URL
	if cfg.Postgres.MigrateURL != "" {
		dbURL = cfg.Postgres.MigrateURL
	}

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	driver := entsql.OpenDB(dialect.Postgres, db)

	client := ent.NewClient(ent.Driver(driver))
	defer client.Close()

	if err := runSeed(ctx, client); err != nil {
		log.Fatalf("failed to seed data: %v", err)
	}

	// Sync tenants so the tenant rows exist before runtime NATS outlet events arrive.
	// Outlets are synced automatically at runtime via auth.outlet.* JetStream events.
	syncer := tenant.NewSyncer(client, cfg.Auth.ServiceURL)
	for _, slug := range []string{"codevertex-demo", "urban-loft", "kura"} {
		tenantID, err := syncer.SyncTenant(ctx, slug)
		if err != nil {
			log.Printf("  [SKIP] sync tenant %s: %v", slug, err)
			continue
		}
		log.Printf("  ✓ Tenant synced: %s", slug)
		if err := seedTenantAdminRole(ctx, client, tenantID, slug); err != nil {
			log.Printf("  [WARN] seed admin role for %s: %v", slug, err)
		}
		if err := seedTenantDriverRole(ctx, client, tenantID, slug); err != nil {
			log.Printf("  [WARN] seed driver role for %s: %v", slug, err)
		}
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

// seedTenantAdminRole ensures a tenant has an "admin" logistics role granting ALL
// permissions, and (for the demo tenant) assigns it to the demo admin user. Without an
// assigned role a tenant admin's HasPermission() always returns false → 403 on fleet
// management (this blocked rider onboarding for codevertex-demo). Idempotent.
func seedTenantAdminRole(ctx context.Context, client *ent.Client, tenantID uuid.UUID, slug string) error {
	permIDs, err := client.LogisticsPermission.Query().IDs(ctx)
	if err != nil {
		return fmt.Errorf("list permissions: %w", err)
	}
	if len(permIDs) == 0 {
		return nil
	}

	role, err := client.LogisticsRole.Query().
		Where(logisticsrole.TenantID(tenantID), logisticsrole.RoleCode("admin")).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		role, err = client.LogisticsRole.Create().
			SetTenantID(tenantID).
			SetRoleCode("admin").
			SetName("Administrator").
			SetDescription("Full logistics access for tenant administrators").
			SetIsSystemRole(true).
			AddPermissionIDs(permIDs...).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create admin role: %w", err)
		}
		log.Printf("    ✓ admin role created for %s (%d permissions)", slug, len(permIDs))
	case err != nil:
		return fmt.Errorf("query admin role: %w", err)
	default:
		// Role exists — attach any permissions it is missing (handles newly-added perms).
		existing, qerr := role.QueryPermissions().IDs(ctx)
		if qerr != nil {
			return fmt.Errorf("query role permissions: %w", qerr)
		}
		have := make(map[uuid.UUID]bool, len(existing))
		for _, id := range existing {
			have[id] = true
		}
		var missing []uuid.UUID
		for _, id := range permIDs {
			if !have[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			if _, uerr := role.Update().AddPermissionIDs(missing...).Save(ctx); uerr != nil {
				return fmt.Errorf("attach missing permissions: %w", uerr)
			}
			log.Printf("    ✓ admin role for %s topped up with %d permissions", slug, len(missing))
		}
	}

	// Demo provisioning: assign the canonical demo admin to the admin role so fleet
	// management works out of the box — only once the user has synced into logistics.
	if slug == "codevertex-demo" {
		adminUser, uerr := client.User.Query().
			Where(user.TenantID(tenantID), user.EmailEQ("admin@demo.codevertexitsolutions.com")).
			Only(ctx)
		if uerr != nil {
			log.Printf("    [SKIP] demo admin not yet synced into logistics users: %v", uerr)
			return nil
		}
		assigned, aerr := client.UserRoleAssignment.Query().
			Where(
				userroleassignment.TenantID(tenantID),
				userroleassignment.UserID(adminUser.ID),
				userroleassignment.RoleID(role.ID),
			).Exist(ctx)
		if aerr != nil {
			return fmt.Errorf("check assignment: %w", aerr)
		}
		if !assigned {
			if _, cerr := client.UserRoleAssignment.Create().
				SetTenantID(tenantID).
				SetUserID(adminUser.ID).
				SetRoleID(role.ID).
				SetAssignedBy(adminUser.ID).
				Save(ctx); cerr != nil {
				return fmt.Errorf("assign admin role to demo admin: %w", cerr)
			}
			log.Printf("    ✓ demo admin assigned the admin role")
		}
	}

	return nil
}

// driverPermissionCodes is the curated permission set granted to the "driver"
// (rider) role. Unlike the admin role, drivers get only what they need to operate:
// view/work their own tasks, view fleet/vehicle/zone/geofence/routing reference
// data, report telemetry, and view their earnings. PermTaskManage in this set is
// what unblocks PATCH /tasks/{id}/status and POST /tasks/{id}/pod for riders.
var driverPermissionCodes = []string{
	"logistics.tasks.view",
	"logistics.tasks.view_own",
	"logistics.tasks.change",
	"logistics.tasks.change_own",
	"logistics.tasks.manage",
	"logistics.tasks.manage_own",
	"logistics.fleet.view",
	"logistics.vehicles.view",
	"logistics.zones.view",
	"logistics.geofences.view",
	"logistics.routing.view",
	"logistics.telemetry.add",
	"logistics.telemetry.view",
	"logistics.telemetry.manage_own",
	"logistics.earnings.view",
	"logistics.earnings.view_own",
}

// seedTenantDriverRole ensures a tenant has a "driver" logistics role granting the
// curated driverPermissionCodes set. Riders map to RoleDriver ("driver") but no such
// role existed, so rbac.GetUserPermissions returned 0 perms → 403 on task status/POD
// updates. Mirrors seedTenantAdminRole: create if missing, else top-up missing perms.
// Idempotent.
func seedTenantDriverRole(ctx context.Context, client *ent.Client, tenantID uuid.UUID, slug string) error {
	permIDs, err := client.LogisticsPermission.Query().
		Where(logisticspermission.PermissionCodeIn(driverPermissionCodes...)).
		IDs(ctx)
	if err != nil {
		return fmt.Errorf("list driver permissions: %w", err)
	}
	if len(permIDs) == 0 {
		log.Printf("    [SKIP] no driver permissions found for %s (run seedPermissions first)", slug)
		return nil
	}

	role, err := client.LogisticsRole.Query().
		Where(logisticsrole.TenantID(tenantID), logisticsrole.RoleCode("driver")).
		Only(ctx)
	switch {
	case ent.IsNotFound(err):
		_, err = client.LogisticsRole.Create().
			SetTenantID(tenantID).
			SetRoleCode("driver").
			SetName("Driver").
			SetDescription("Curated logistics access for riders/drivers").
			SetIsSystemRole(true).
			AddPermissionIDs(permIDs...).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("create driver role: %w", err)
		}
		log.Printf("    ✓ driver role created for %s (%d permissions)", slug, len(permIDs))
	case err != nil:
		return fmt.Errorf("query driver role: %w", err)
	default:
		// Role exists — attach any curated permissions it is missing.
		existing, qerr := role.QueryPermissions().IDs(ctx)
		if qerr != nil {
			return fmt.Errorf("query driver role permissions: %w", qerr)
		}
		have := make(map[uuid.UUID]bool, len(existing))
		for _, id := range existing {
			have[id] = true
		}
		var missing []uuid.UUID
		for _, id := range permIDs {
			if !have[id] {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			if _, uerr := role.Update().AddPermissionIDs(missing...).Save(ctx); uerr != nil {
				return fmt.Errorf("attach missing driver permissions: %w", uerr)
			}
			log.Printf("    ✓ driver role for %s topped up with %d permissions", slug, len(missing))
		}
	}

	return nil
}
