# logistics-api -- Architecture

**Service**: logistics-api (Go)
**Deployed**: logisticsapi.codevertexitsolutions.com
**Port**: 4005
**Canonical tenant**: `urban-loft` | **Active outlet**: Busia

---

## Stack

| Layer | Technology |
|-------|-----------|
| Language | Go 1.22+ |
| HTTP | Chi router, shared-auth-client middleware |
| ORM | Ent (Facebook) -- schema-driven migrations |
| Database | PostgreSQL 16 + PostGIS |
| Cache / Pub-sub | Redis 7, NATS JetStream |
| Observability | Zap logger, Prometheus `/metrics` |
| Auth | JWT (JWKS from auth-service) + API-key for S2S |
| Event relay | Outbox table + background publisher |
| Migrations | Ent auto-migrate (transitioning to Atlas for versioned migrations) |

### Atlas migration transition

The current setup uses `client.Schema.Create()` (Ent auto-migrate). Before MVP launch, migrate to Atlas versioned migrations:

1. Generate baseline: `atlas migrate diff --env ent`
2. Store migrations in `internal/ent/migrate/migrations/`
3. Run via CI: `atlas migrate apply --url $DATABASE_URL`
4. Remove `Schema.Create()` from seed/boot paths

---

## Directory layout

```
logistics-api/
  cmd/
    api/main.go              -- entry point
    seed/main.go             -- DB seed
  internal/
    app/app.go               -- bootstrap, DI, server wiring
    config/config.go         -- envconfig (LOGISTICS_ prefix)
    ent/
      schema/                -- Ent schema definitions
      migrate/               -- generated migration schema
    http/
      handlers/              -- Chi handlers per domain
      router/router.go       -- route tree, middleware
      docs/                  -- Swagger/OpenAPI
    modules/
      fleet/                 -- fleet service + repo
      task/                  -- task service + repo
      tracking/              -- Redis-based location tracking
      outbox/                -- outbox repo (pgx)
    platform/
      events/                -- NATS connection, outbox publisher
      subscriber/            -- NATS consumers (ordering, auth)
    services/
      rbac/                  -- RBAC service (in-memory seed)
      usersync/              -- auth-service user sync
    shared/logger/           -- Zap wrapper
  docs/                      -- architecture, ERD, sprints
```

---

## Multi-tenancy model

All operational tables carry `tenant_id` (UUID) and `tenant_slug` (string). Routes are scoped:

```
/api/v1/{tenantSlug}/tasks
/api/v1/{tenantSlug}/fleets
/api/v1/{tenantSlug}/admin/riders
```

Tenant metadata synced from auth-service via `auth.tenant.created` / `auth.tenant.updated` events.

**Tenant/brand for UIs**: Logistics-ui uses tenant slug from `[orgSlug]` or `NEXT_PUBLIC_TENANT_SLUG`. Tenant info: auth-api `GET /api/v1/tenants/by-slug/{slug}`. Branding: notifications-api `GET /api/v1/{tenantId}/branding` (use tenant id from auth response). Reuse notifications-ui `BrandingProvider` pattern.

### Platform admin vs tenant admin

| Actor | Scope | Mechanism |
|-------|-------|-----------|
| Platform admin (superuser) | Cross-tenant | `X-API-Key` header, no tenant path segment needed |
| Tenant admin | Single tenant | JWT with `tenant_id` claim, path `{tenantSlug}` validated |
| Rider / fleet member | Single tenant | JWT, access limited to own profile and assigned tasks |

---

## Multi-outlet awareness

Outlets are referenced via `auth.outlet.created` events. Tasks carry pickup/dropoff outlet context. Current MVP scope: single outlet (Busia) under `urban-loft`.

Post-MVP, outlet-level dispatch rules and zone fencing will gate task routing per outlet.

---

## API surface

### Public

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/healthz` | Liveness |
| GET | `/readyz` | Readiness (Postgres, Redis, NATS) |
| GET | `/metrics` | Prometheus |
| GET | `/v1/docs/*` | Swagger UI |

### Authenticated (JWT required)

| Domain | Endpoints |
|--------|-----------|
| Tasks | `POST/GET /api/v1/{t}/tasks`, `GET /{taskId}`, `POST /{taskId}/assign`, `POST /{taskId}/cancel`, `PUT /{taskId}/status` |
| Tracking | `POST /api/v1/{t}/tracking/rider/location`, `GET /rider/{riderId}/location`, `GET /task/{taskId}` |
| Deliveries | `POST/GET /api/v1/{t}/deliveries/{taskId}/proof` |
| Fleets | `POST/GET /api/v1/{t}/fleets`, `GET/PUT/DELETE /{id}` |
| Admin riders | `POST /api/v1/{t}/admin/riders/invite`, `GET/PUT /{id}`, approve/suspend/reject |
| RBAC | `POST/GET/DELETE /api/v1/{t}/rbac/assignments` |
| Users | `POST /api/v1/{t}/users`, `GET /me/roles`, `GET /roles` |
| Rider self | `GET /api/v1/riders/me`, `PATCH /me/profile` |

---

## Database schema (Ent)

Implemented tables: `fleets`, `fleet_members`, `vehicles`, `tasks`, `logistics_roles`, `logistics_role_assignments`, `outbox_events`.

Planned (documented in `erd.md`): `vehicle_documents`, `rider_shifts`, `task_steps`, `task_events`, `task_assignments`, `task_documents`, `dispatch_rules`, `dispatch_batches`, `routes`, `route_segments`, `route_metrics`, `telemetry_streams`, `telemetry_points`, `geo_fence_events`, `telemetry_alerts`, `proof_of_delivery`, `customer_feedback`, `delivery_incidents`, `geo_fences`, `delivery_zones`, `tariff_profiles`, `tariff_rules`, `earnings`, `expense_claims`.

PostGIS columns (`GEOGRAPHY(Point, 4326)`) planned for `telemetry_points.geo_point`, `geo_fence_events.geo_point`, `geo_fences.geometry`, `delivery_zones.boundary`.

---

## Event architecture

### NATS stream: `logistics` (subjects: `logistics.*`)

**Published (via outbox)**:

| Event | Trigger |
|-------|---------|
| `logistics.task.created` | New task |
| `logistics.task.assigned` | Rider assigned |
| `logistics.task.accepted` | Rider accepted |
| `logistics.task.en_route` | Rider en route |
| `logistics.task.completed` | Delivery done |
| `logistics.task.cancelled` | Task cancelled |

**Consumed**:

| Event | Action |
|-------|--------|
| `ordering.order.ready` | Create delivery task from order |
| `auth.user.created` | Create fleet member if rider role |
| `auth.tenant.created` | Initialize tenant |
| `auth.outlet.created` | Register outlet |

### Outbox pattern

1. Handler writes `outbox_events` row in same DB transaction
2. Background publisher polls pending rows, publishes to NATS JetStream
3. Marks rows as `published` on success; retries with backoff on failure

---

## RBAC & auth

**Roles/permissions source of truth:** auth-service (auth-api). Frontend (logistics-ui) loads user and RBAC from auth-api `GET /me` with TanStack Query and TTL for nav visibility and route protection. Logistics-api has local RBAC (logistics_roles, logistics_role_assignments) and in-memory default roles in `internal/services/rbac` for service-specific permission checks; role/permission seed is not duplicated here—auth-api remains the source of truth. RBAC handlers: assign/revoke/list assignments; repository may sync from auth or use local tables per tenant.

---

## Location tracking (Redis)

- `POST /tracking/rider/location` writes to Redis sorted set keyed by `rider:{riderId}:locations`
- `GET /tracking/rider/{riderId}/location` reads latest from Redis
- `GET /tracking/task/{taskId}` resolves assigned rider, returns location

WebSocket upgrade planned for real-time streaming to logistics-ui and cafe-website.

---

## MVP scope (March 17, 2026)

- Task CRUD, assignment, status flow (pending -> assigned -> picked_up -> completed)
- Fleet and fleet member management
- Rider self-service profile (`/riders/me`)
- Admin rider lifecycle (invite, approve, suspend, reject)
- Proof of delivery (photo + signature upload)
- Redis-based location tracking
- Outbox-driven event publishing
- RBAC (dispatcher, rider, hub_operator, tenant_admin)
- Auth integration (JWT + user sync)

### Post-MVP

- PostGIS geo-queries for zone-based dispatch
- Telemetry streaming and analytics
- Route optimization (OSRM/Mapbox)
- Earnings calculation and treasury integration
- Delivery zone configuration
- SLA monitoring and breach alerts
