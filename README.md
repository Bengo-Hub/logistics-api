# Logistics Service

**Live in production** at `https://logisticsapi.codevertexafrica.com`

The Logistics Service coordinates dispatch, routing, telemetry, and carrier integrations for Codevertex deliveries, inventory transfers, and reverse logistics using the same multi-tenant `tenant_slug` and outlet registry shared across the platform.

## What's Implemented

| Area | Status |
|------|--------|
| Fleet & rider CRUD | ✅ Live |
| Rider invite / KYC approval workflow | ✅ Live |
| Task lifecycle FSM (create→assign→accept→en_route→completed) | ✅ Live |
| Auto-dispatch (nearest rider via Valhalla + Redis GEO) | ✅ Live |
| Manual dispatch endpoint | ✅ Live |
| Proof of Delivery (photo, signature, OTP, COD) | ✅ Live |
| NATS ordering.order.ready → auto-create task | ✅ Live |
| NATS inventory.transfer.created → auto-create transfer task | ✅ Live |
| Outbox events (task.assigned, completed, sla_breached, …) | ✅ Live |
| ETA calculation + recalculation on status change | ✅ Live |
| Real-time task SSE streaming (`/tasks/{id}/stream`) | ✅ Live |
| SLA monitor job + breach events (5-min scan) | ✅ Live |
| GPS telemetry ingestion + Redis GEO update | ✅ Live |
| Valhalla routing (route, ETA, matrix, isochrone) | ✅ Live |
| Geo-fence zones CRUD | ✅ Live |
| RBAC (roles, permissions, enforcement middleware) | ✅ Live |
| Earnings calculator + billing events + statements | ✅ Live |
| Pricing rules CRUD | ✅ Live |
| Rate limiting (Redis sliding window) | ✅ Live |
| Atlas versioned migrations with embedded FS fallback | ✅ Live |
| Prometheus metrics + OTLP traces | ✅ Live |
| WebSocket tracking (fleet-wide live map) | ⬜ Sprint 4 |
| Geofence event triggers | ⬜ Sprint 4 |
| Tariff profiles + treasury export | ⬜ Sprint 7 |
| Reverse logistics / returns | ⬜ Sprint 8 |

## Technology

- **Go 1.22+**, Ent ORM, PostgreSQL 16 + PostGIS, Redis 7
- **Routing**: self-hosted Valhalla v3.5.1 (`routing.codevertexafrica.com`)
- **REST API**: chi v5 router, JWT auth via shared-auth-client (JWKS)
- **Events**: NATS JetStream, outbox pattern for reliable delivery
- **Observability**: zap logs, Prometheus metrics, OpenTelemetry traces (OTLP)
- **Deployments**: ArgoCD GitOps → K8s (logistics namespace)

## Local Development

```shell
# Set up env
cp config/example.env .env
# edit .env: set POSTGRES_URL, REDIS_ADDR, EVENTS_NATS_URL, AUTH_JWKS_URL

# Run migrations
go run ./cmd/migrate

# Seed permissions and rate limit configs
go run ./cmd/seed

# Start the API server
go run ./cmd/api
```

### Generating Ent code + Atlas migrations

```shell
# After editing internal/ent/schema/*.go
go generate ./internal/ent

# Create a new Atlas migration (requires local PostgreSQL)
atlas migrate diff <migration_name> \
  --dir "file://internal/ent/migrate/migrations" \
  --to "ent://internal/ent/schema" \
  --dev-url "postgres://postgres:password@localhost:5432/logistics_dev?sslmode=disable"

# Recalculate atlas.sum
atlas migrate hash --dir "file://internal/ent/migrate/migrations"
```

## Key Environment Variables

| Variable | Description |
|----------|-------------|
| `POSTGRES_URL` | PostgreSQL connection string |
| `REDIS_ADDR` | Redis address (host:port) |
| `EVENTS_NATS_URL` | NATS JetStream URL(s) |
| `AUTH_JWKS_URL` | JWKS endpoint from auth-api |
| `AUTH_ISSUER` | JWT issuer (must match token) |
| `ROUTING_PRIMARY_URL` | Valhalla base URL |
| `MEDIA_ROOT` | Local path for uploaded files |
| `MEDIA_URL_BASE` | Public base URL for media files |

The service binds to `http://localhost:4103` by default (configurable via `LOGISTICS_HTTP_PORT`).

## Structure

- `cmd/` – main binaries (`server`, `migrate`, `worker`, `dispatcher`).
- `internal/app` – bootstrap and dependency wiring.
- `internal/ent` – Ent schemas and generated clients.
- `internal/modules` – fleet, dispatch, tasks, telemetry, billing, integrations.
- `docs/` – ERD, ADRs, integration playbooks, incident runbooks.
  - [ERD overview](./docs/erd.md)
  - [Cross‑service ERD alignment](./docs/erd-alignment.md)
  - [API contract (OpenAPI)](./docs/api/openapi.yaml)
  - [Mapping & routing providers](./docs/integrations/mapping-providers.md)
  - [Threat model](./docs/threat-model.md)
  - [Sprint 0 progress](./docs/sprints/sprint-0.md)

## Integrations

- **Food Delivery Backend:** task creation, ETA updates, proof-of-delivery, escalation signals.
- **Inventory Service:** transfer orders, pick wave coordination, warehouse pickups.
- **POS Service:** curbside pickup readiness, in-store dispatch requests.
- **Notifications Service:** customer and rider alerts, SLA breach notifications.
- **Treasury App:** payouts, tariffs, marketplace billing delivered via webhook callbacks (no polling).
- **Auth Service:** rider SSO, device sessions, token validation; tenant/outlet discovery callbacks hydrated here on first login.

Refer to `plan.md` and `docs/erd.md` for the current design blueprint.

## Current Status

- In planning/design phase; schema modelling in progress.
- Major milestones tracked in `CHANGELOG.md`.

