# Sprint 0 – Platform Foundations (Week 1)

**Status**: In Progress  
**Start Date**: 2025-01-17  
**Target Completion**: 2025-01-24  
**Progress note (2026-03-06):** Auth-Service SSO (JWT via shared-auth-client) completed. API contract, ERD, threat model, and sprint docs in place. Remaining: full scaffolding, Ent schemas, CI/CD, provider registry.

**RBAC (MVP):** This service has no local Role/Permission schema. Authorization is delegated to **auth-api JWT**: protected routes validate the Bearer token and use claims (tenant_id, user_id, scopes/permissions) from auth-api. When Ent schemas and `cmd/seed` are added (e.g. in 1.2), the seed MUST create permissions for resources **tasks**, **riders**, **fleet** using the MVP action set: add, read, read_own, change, change_own, delete, manage, manage_own (e.g. `tasks:read`, `riders:manage`, `fleet:read_own`). Core data to seed: demo tenant, demo fleet, default config. See shared-docs/mvp-critical-path.md §8.

## Goals

- Establish project scaffolding, CI/CD, environments, and security baselines.
- Define contracts and align core entities with dependent services (Inventory, POS, Treasury, Auth).
- Set up foundational infrastructure for multi-tenant, event-driven architecture.

## Detailed Tasks

### 1.1 Repository Scaffolding & Configuration
- [ ] Initialize Go module (`go mod init github.com/bengobox/logistics-service`)
- [ ] Create directory structure:
  - `cmd/` (server, migrate, worker, dispatcher binaries)
  - `internal/app/` (bootstrap and dependency wiring)
  - `internal/ent/` (Ent schemas and generated clients)
  - `internal/modules/` (fleet, dispatch, tasks, telemetry, billing, integrations)
  - `docs/` (ERD, ADRs, integration playbooks)
  - `config/` (example.env, Helm values)
- [ ] Set up structured logging with `zap`:
  - JSON output for production, console for local dev
  - Log levels configurable via env (`LOG_LEVEL`)
  - Request ID middleware for traceability
- [ ] Health and liveness endpoints:
  - `GET /health` → returns 200 if service is up
  - `GET /ready` → returns 200 if DB/Redis connections are healthy
  - `GET /metrics` → Prometheus metrics endpoint
- [ ] Configuration loader using `envconfig`:
  - Load from `.env` (local) or environment variables (K8s)
  - Validate required configs on startup
  - Feature flags via `FEATURE_*` env vars

### 1.2 Ent Base Schemas
- [ ] Install Ent: `go get entgo.io/ent/cmd/ent`
- [ ] Create base schemas in `internal/ent/schema/`:
  - `tenant.go` → `tenant_id`, `tenant_slug`, `name`, `status`, `metadata`
  - `outlet.go` → `outlet_id`, `tenant_id`, `name`, `address_json`, `geo_point` (PostGIS)
  - `fleet.go` → `tenant_id`, `tenant_slug`, `name`, `type`, `status`, `metadata`
  - `fleet_member.go` → `tenant_id`, `fleet_id`, `user_id` (ref to auth-service), `driver_code`, `status`, `vehicle_id`
- [ ] Generate Ent client: `go generate ./internal/ent`
- [ ] Create migration tooling:
  - `cmd/migrate/main.go` → uses `ent/migrate` to apply schema changes
  - Migration files stored in `migrations/` directory
- [ ] Seed data script:
  - Demo tenant (`urban_cafe`)
  - Demo fleet (`urban_internal`)
  - Reference outlet IDs (from auth-service discovery webhooks)

### 1.3 Auth-Service SSO Integration
- [ ] JWT middleware for `chi` router:
  - Extract `Authorization: Bearer <token>` header
  - Validate token signature using auth-service public key (fetched on startup or via JWKS endpoint)
  - Extract claims: `tenant_id`, `tenant_slug`, `user_id`, `scopes`
  - Inject claims into request context for downstream handlers
- [ ] OAuth2 client credentials flow:
  - Service account credentials stored in secrets (Vault/K8s secrets)
  - Token exchange endpoint: `POST /v1/auth/token` (internal only)
  - Cache tokens in Redis with TTL
- [ ] Scope guards middleware:
  - `RequireScope(scope string)` → validates user/service has required scope
  - Enforce per-route: e.g., `logistics.tasks.create`, `logistics.fleet.read`
- [ ] Tenant/outlet discovery webhook handler:
  - `POST /v1/webhooks/tenant-discovery` → receives tenant/outlet metadata from auth-service
  - Store in `tenant_sync_events` table (see ERD)
  - Validate `tenant_id`/`tenant_slug` before persisting tasks or fleet members

### 1.4 CI/CD & Deployment
- [ ] GitHub Actions workflow (`.github/workflows/ci.yml`):
  - Lint: `golangci-lint run`
  - Test: `go test ./...`
  - Build: `docker build -t logistics-service:${{ github.sha }}`
  - Push to container registry (GHCR or Docker Hub)
- [ ] Dockerfile (multi-stage):
  - Builder stage: Go 1.22+, compile binary
  - Runtime stage: Alpine, copy binary, non-root user
  - Expose port 4103 (configurable via `LOGISTICS_HTTP_PORT`)
- [ ] Helm chart (`charts/logistics-service/`):
  - Deployment with HPA defaults (min: 2, max: 10, target CPU: 70%)
  - Service (ClusterIP), Ingress (optional)
  - ConfigMap for env vars, Secret for sensitive data
  - VPA (Vertical Pod Autoscaler) recommendations enabled
- [ ] Secrets management:
  - Integration with Vault or K8s Secrets
  - Provider API keys encrypted at rest (see 1.5)
  - Rotation job scaffolding (future sprint)
- [ ] OpenAPI baseline:
  - Generate from code annotations using `swaggo/swag`
  - Publish to `docs/api/openapi.yaml`
  - Serve at `GET /swagger/index.html` (dev only)

### 1.5 Provider Registry & Secrets
- [ ] Provider registry schema (`internal/ent/schema/provider.go`):
  - `provider_credentials` table: `id`, `tenant_id`, `provider_code` (e.g., `google_maps`, `osrm`, `openweather`), `encrypted_credentials_json`, `status`, `created_at`, `updated_at`
- [ ] Encryption primitives:
  - Envelope encryption: generate data encryption key (DEK) per tenant/provider
  - Encrypt DEK with master key (from KMS/Vault)
  - Store encrypted DEK + encrypted credentials in DB
  - Decrypt on read (redact in API responses)
- [ ] Provider configuration API stubs:
  - `GET /v1/{tenant}/integrations/providers` → list enabled providers
  - `POST /v1/{tenant}/integrations/providers` → register new provider
  - `GET /v1/{tenant}/integrations/providers/{provider}/config` → get config (redacted)
  - `PUT /v1/{tenant}/integrations/providers/{provider}/config` → update config (encrypt before store)

## Progress Log

- **2025-01-17**:
  - Created API contract skeleton: `docs/api/openapi.yaml`
  - Drafted mapping provider integration notes: `docs/integrations/mapping-providers.md`
  - Updated ERD with comprehensive cross-service entity alignment: `docs/erd.md` (includes entity ownership rules, integration patterns, and zone/dispatch collaboration)
  - Initial Threat Model: `docs/threat-model.md`
  - Created comprehensive sprint documentation files (sprint-0 through sprint-10) in `docs/sprints/`
  - Updated `plan.md` to link to sprint files instead of duplicating task lists
  - Updated `README.md` with documentation links and `CHANGELOG.md` with Sprint 0 kickoff entry

## Dependencies

- **Auth Service**: Must be running for JWT validation and tenant discovery webhooks
- **PostgreSQL**: Database for Ent schemas (with PostGIS extension for geospatial)
- **Redis**: Caching and session storage (optional for Sprint 0, required for Sprint 1+)

## Risks & Mitigations

- **Mapping provider quotas**: Add rate limiting and fallback (OSRM) – see `docs/integrations/mapping-providers.md`
- **Data model drift with Inventory/POS**: ERD alignment document (`docs/erd.md`) and shared IDs/policies ensure consistency
- **Auth-service downtime**: Implement token caching and graceful degradation (log warnings, don't fail startup)

## Acceptance Criteria

- [ ] Service starts successfully with health/ready endpoints responding
- [ ] Ent schemas generate without errors, migrations run cleanly
- [ ] JWT middleware validates tokens from auth-service
- [ ] CI/CD pipeline passes (lint, test, build)
- [ ] Helm chart deploys to staging cluster
- [ ] Provider registry can store/retrieve encrypted credentials

## Next Sprint Preview

Sprint 1 will build on these foundations to implement fleet and rider management CRUD operations, service-level RBAC, and device registry.
