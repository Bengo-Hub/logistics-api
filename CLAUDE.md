# Logistics API – Claude Code Guide

## Service
Go REST API for fleet/rider management, task dispatch, GPS telemetry, routing, and earnings.  
**Production**: `https://logisticsapi.codevertexafrica.com`  
**K8s namespace**: `logistics`  
**Repo**: `github.com/Bengo-Hub/logistics-api`

## Architecture
- **Framework**: Chi v5 router, hexagonal structure: handlers → service → Ent ORM
- **DB**: PostgreSQL 16 + PostGIS, managed via Atlas versioned migrations
- **Cache**: Redis (rider locations via GEO, rate limiting, ETA cache)
- **Events**: NATS JetStream with outbox pattern (`/internal/platform/events`)
- **Auth**: `shared-auth-client` JWKS validation; platform owners bypass tenant checks

## Key Directories
```
cmd/
  api/          # HTTP server entrypoint
  migrate/      # Atlas migration binary (runs on pod startup)
  seed/         # Permission + rate-limit seeding
internal/
  app/          # App wiring (NewApp), all module initialization
  config/       # Config struct (env-driven, prefix "")
  ent/          # Generated Ent ORM code + schemas + migrations
  http/
    handlers/   # HTTP handlers (one file per domain)
    router/     # router.New() — all routes registered here
  middleware/   # Auth, rate limiting, RBAC enforcement, outlet context
  modules/
    dispatch/   # AutoDispatcher, ETAUpdater, BatchScheduler
    earnings/   # EarningsService, calculator, pricing rules
    fleet/      # FleetService, stale-rider cleanup
    rbac/       # RBACService, permission enforcement
    routing/    # Valhalla client
    tasks/      # TaskService, SLAMonitor, FSM
    telemetry/  # TelemetryService (GPS ingestion)
  platform/
    events/     # NATS publisher (outbox)
```

## Development Commands
```bash
go build ./...           # compile check
go generate ./internal/ent  # regenerate Ent code after schema changes
go run ./cmd/api         # start server (needs POSTGRES_URL, REDIS_ADDR, etc.)
go run ./cmd/migrate     # run Atlas migrations
go run ./cmd/seed        # seed permissions + rate limit configs
```

## Atlas Migration Workflow
```bash
# 1. Edit schema in internal/ent/schema/*.go
# 2. Regenerate Ent code
go generate ./internal/ent

# 3. Generate migration SQL (needs local PostgreSQL)
atlas migrate diff <name> \
  --dir "file://internal/ent/migrate/migrations" \
  --to "ent://internal/ent/schema" \
  --dev-url "postgres://postgres:postgres@localhost:5432/logistics_dev?sslmode=disable"

# 4. Recalculate checksum
atlas migrate hash --dir "file://internal/ent/migrate/migrations"

# 5. Commit schema changes + new migration SQL + atlas.sum
```

## RBAC & Permissions
Permission codes: `logistics.<module>.<action>` (e.g. `logistics.tasks.manage`)  
All mutation routes are protected via `appmw.RequirePermission(rbacSvc, rbac.PermXxx)`.  
Permissions seeded at startup by `cmd/seed`. Platform owners bypass all checks.

## Key Rules
- Image tags in devops-k8s values.yaml are set by build.sh only — never edit manually
- All S2S calls use `INTERNAL_SERVICE_KEY` (X-API-Key header), single shared key
- Ent code is generated — never edit generated files under `internal/ent/` directly
- Atlas migrations are append-only — never edit existing .sql files
- Use `go build ./...` to verify before pushing — CI blocks on build errors
