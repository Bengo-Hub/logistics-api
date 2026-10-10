# Logistics API — Integrations

**Service**: logistics-api (Go)  
**Canonical data ownership**: See **shared-docs/CROSS-SERVICE-DATA-OWNERSHIP.md** for the cross-service entity ownership matrix and reference-only rules.

---

## Overview

Logistics-api owns **fleets, riders (fleet members), vehicles, tasks, routes, proof of delivery, and telemetry**. It does **not** own orders, users, tenants, inventory, or payments; it references them by ID and consumes events or REST from other services.

---

## Inbound: Ordering-Backend (Cafe / Online Orders)

**How ordering-backend integrates with logistics-api:**

| Flow | Mechanism | Description |
|------|------------|-------------|
| **Create delivery task** | REST `POST /api/v1/{tenant}/tasks` | When an order is ready for delivery, ordering-backend calls logistics-api to create a task. Payload includes `external_reference` (order ID), pickup/dropoff locations, customer contact, instructions. |
| **Webhook callbacks** | HTTP `POST /api/v1/webhooks/logistics` (on ordering-backend) | Logistics-api does **not** call ordering-backend; instead, logistics-api publishes NATS events (`logistics.task.assigned`, `logistics.task.completed`, etc.). Ordering-backend **receives webhooks from logistics** (or subscribes to NATS) to update its local `order_assignments` (e.g. rider_id, status). |
| **Event-driven task creation** | NATS `ordering.order.ready` or `cafe.order.ready` | Logistics-api subscribes to order-ready events and can create a task when an order is ready for delivery. Ordering-backend may also create the task via REST (see plan/integrations in ordering-backend). |
| **Tracking & PoD** | REST `GET /api/v1/{tenant}/tracking/task/{taskId}`, `GET .../deliveries/{taskId}/proof` | Ordering-backend (or ordering-frontend) calls logistics-api to get live rider location and proof of delivery. |

**Data ownership:** Ordering-backend stores only `order_assignments.logistics_task_id` and `order_assignments.rider_id` (references). All task, rider, fleet, and PoD data are owned by logistics-api.

---

### Delivery quotes (added 2026-10-09)

Ordering no longer prices deliveries itself. At checkout it calls `POST /api/v1/s2s/zones/{tenant}/quote` with the drop-off pin and outlet, charges the returned fee, and sends `delivery_zone_id`, `delivery_zone_name` and `distance_km` in `ordering.order.ready`. Customer UIs use the public coverage, quote and geocode endpoints. Full contract in `docs/delivery-zones.md`.

### POS till deliveries (added 2026-10-10)

pos-api prices a till delivery the same way. The terminal's delivery panel lists the tenant's areas (`GET /s2s/zones/{tenant}/coverage` through pos-api's `/pos/delivery-areas`), shows the fee from `/pos/delivery-quote`, and on save pos-api re-quotes the pin and charges the fee as `charges.shipping` (snapshot in `metadata.delivery_quote`). pos-api keeps no fee tables.

## Outbound: ERP-API (staff riders, added 2026-10-10)

Riders are either `freelance` (paid per delivery from pricing rules) or `staff` (employees on erp-api payroll), stored in `fleet_members.metadata.employment` and set from the add-rider form or `PUT /{tenant}/fleet/members/{id}/employment`. Staff riders earn nothing per task unless `per_task_earnings` is set.

Salary and per diem belong to erp-api. When a staff rider completes a task, logistics calls `POST {ERP_SERVICE_URL}/api/v1/hrm/claims/external` (X-API-Key + X-Tenant-ID) with the rider's auth user id, trip distance, start and end, and `source_key = logistics:task:<id>:per_diem`. erp-api applies its `hrm.per_diem_policy` (daily rate, job group rates, minimum distance, max days), creates a normal expense claim for HR approval and payroll, or answers 422 (`not_an_employee`, `per_diem_disabled`, `not_eligible`). The outcome is kept on the task as `metadata.per_diem_claim`. Admins (`POST /{tenant}/tasks/{id}/per-diem`) and the rider (`POST /{tenant}/riders/me/tasks/{id}/per-diem`) can raise it again, with the number of days for a multi-day trip; erp-api returns the existing claim for the same task.

## Inbound: Auth-Service

- **JWT validation**: All protected routes require a valid Bearer token from auth-service (JWKS).
- **Tenant / user identity**: Logistics does not store users or tenants; it uses `tenant_id` and `user_id` from JWT and syncs tenant metadata via `auth.tenant.created` / `auth.tenant.updated` into `tenant_sync_events`.
- **Fleet members**: `fleet_members.user_id` is a reference to auth-service user. Rider onboarding uses auth-service for login; logistics stores only rider-specific data (vehicle, documents, KYC).

---

## Inbound: Inventory, POS, Notifications, Treasury

- **Inventory**: Webhooks `inventory.transfer.created`, `inventory.transfer.completed`; REST for availability (e.g. zone/branch) when needed for dispatch. See plan.md §2.5.
- **POS**: Webhooks `pos.order.ready`, `pos.order.handoff` for pickup/curbside flows.
- **Notifications**: Outbound only — logistics publishes events that notifications-api consumes for customer ETA and SLA alerts.
- **Treasury**: REST for expenses, payouts, bills; events `treasury.payout.completed`, `treasury.expense.approved`. See plan.md §2.5.

---

## Outbound: Events (NATS)

**Published by logistics-api (via outbox):**

| Event | When | Consumers |
|-------|------|-----------|
| `logistics.task.created` | Task created | Ordering-backend, notifications |
| `logistics.task.assigned` | Rider assigned | Ordering-backend (updates order_assignment) |
| `logistics.task.accepted` | Rider accepted | Ordering-backend |
| `logistics.task.en_route` | Rider en route | Ordering-backend, notifications (ETA) |
| `logistics.task.completed` | Delivery completed | Ordering-backend, notifications |
| `logistics.task.cancelled` | Task cancelled | Ordering-backend |
| `logistics.route.updated` | ETA/route updated | Ordering-backend, notifications |

**Consumed by logistics-api:**

| Event | Action |
|-------|--------|
| `ordering.order.ready` / `cafe.order.ready` | Create delivery task from order (if not already created via REST) |
| `auth.user.created` | Optionally create fleet member if rider role |
| `auth.tenant.created` / `auth.tenant.updated` | Initialize/update tenant metadata |
| `auth.outlet.created` | Register outlet for task steps |

---

## Module Enablement System (Added 2026-05-28)

Logistics-api serves three distinct business use cases. Module gating controls which features are visible per tenant.

### Use Cases & Default Module Sets

| `use_case` | Default Modules |
|------------|----------------|
| `courier` | `dashboard`, `fleet`, `dispatch`, `tracking`, `analytics`, `earnings`, `vehicles`, `settings`, `rbac` |
| `delivery` | `dashboard`, `fleet`, `dispatch`, `tracking`, `analytics`, `earnings`, `pricing`, `settings`, `rbac` |
| `distribution` | `dashboard`, `fleet`, `dispatch`, `tracking`, `distribution`, `cold_chain`, `smart_locks`, `analytics`, `earnings`, `vehicles`, `settings`, `rbac` |

### Module Resolution in `/auth/me`

The `GET /{tenant}/auth/me` response now includes:

```json
{
  "enabled_modules": ["dashboard", "fleet", "dispatch", "tracking", ...],
  "use_case": "courier"
}
```

`permissions` lists every logistics permission for tenant `admin`/`superuser` tokens and platform owners (the same rule `RequirePermission` applies), so tenant admins need no logistics role row. Role grants under `/{tenant}/rbac/assignments` require `logistics.config.manage`.

**Resolution order:**
1. Explicit `ServiceConfig` override (`key = "logistics.enabled_modules"`, stored as JSON array)
2. `use_case` defaults from `consts/modules.go`
3. All modules (platform owner / fallback)

Platform admins can override modules via `PUT /{tenant}/service-config` with `{"key": "logistics.enabled_modules", "value": ["module1", "module2"]}`.

### Analytics KPI Endpoint (Added 2026-05-28)

`GET /{tenant}/analytics/kpis?period=today|7d|30d`

Returns computed KPIs:

```json
{
  "period": "7d",
  "total_tasks": 120,
  "pending_tasks": 5,
  "active_tasks": 8,
  "completed_tasks": 100,
  "failed_tasks": 4,
  "cancelled_tasks": 3,
  "on_time_percent": 91.7,
  "active_riders": 12,
  "total_riders": 18,
  "utilization_percent": 66.7,
  "avg_delivery_minutes": 32.4
}
```

---

## Map Services Integration (Valhalla + TileServer)

Logistics-api wraps the self-hosted Valhalla routing engine with Redis caching, provider fallback, and tenant-scoped rate limiting. The map tile server (TileServer-GL) serves OpenStreetMap vector tiles to all frontends via `@bengo-hub/maps`.

### Routing API Endpoints (via logistics-api)

All routing endpoints require authentication and are scoped to the tenant's subscription plan rate limits.

| Endpoint | Method | Description | Rate Limit Key |
|----------|--------|-------------|----------------|
| `/{tenant}/routing/route` | GET | Route between two points (ETA + distance + polyline) | `routing_requests_per_day` |
| `/{tenant}/routing/eta` | GET | ETA in minutes + distance in km | `routing_requests_per_day` |
| `/{tenant}/routing/matrix` | POST | N×M distance/duration matrix | `routing_requests_per_day` |
| `/{tenant}/routing/isochrone` | GET | Reachability polygon (time-based) | `routing_requests_per_day` |
| `/{tenant}/routing/health` | GET | Routing provider health status | — |

**Query parameters** (route/eta): `from_lat`, `from_lng`, `to_lat`, `to_lng`
**Query parameters** (isochrone): `lat`, `lng`, `time_minutes` (default 15)
**Request body** (matrix): `{"origins": [{"lat":..,"lng":..}], "destinations": [{"lat":..,"lng":..}]}`

### Public Tracking Endpoint

| Endpoint | Method | Auth | Description |
|----------|--------|------|-------------|
| `/api/v1/track/{trackingCode}` | GET | None | Public order/delivery tracking by waybill or tracking code |

Returns: status, status_history timeline, rider info, pickup/dropoff locations, `live_tracking_available` flag.

### Infrastructure

| Component | Internal URL | External URL |
|-----------|-------------|--------------|
| Valhalla | `http://valhalla.logistics.svc.cluster.local:8002` | `https://routing.codevertexafrica.com` |
| TileServer | `http://tileserver.logistics.svc.cluster.local:8080` | `https://tiles.codevertexafrica.com` |

### Rate Limiting (per tenant subscription plan)

Limits come from the token (`subscription_limits`) and count per calendar month (shared-ratelimit `NewMonthlyQuota`). The keys match the subscriptions catalog:

| Limit | Tier 1 | Tier 2 | Tier 3 |
|---------|---------|--------|--------------|
| `routing_requests_per_month` | 100 | 500 | Unlimited |
| `live_tracking_requests_per_month` | none (feature is tier 3) | none (feature is tier 3) | Unlimited |

Live GPS tracking (`live_tracking`: fleet map, WebSocket streams) and route optimisation are tier 3 features. When a limit is reached the API returns `HTTP 429` with `X-RateLimit-*` headers, `Retry-After` until the month resets, and an upgrade URL.

### Frontend Integration (@bengo-hub/maps)

All frontends use the shared `@bengo-hub/maps` NPM package (MapLibre GL JS) which connects to:
- **TileServer** for map rendering (vector tiles)
- **Logistics-API** routing endpoints for directions, ETA, distance

---

## Configuration

| Variable | Description |
|---------|-------------|
| `AUTH_SERVICE_URL` | Auth-service base URL (JWKS, tenant sync) |
| `NATS_URL` | NATS JetStream for event publish/subscribe |
| `ROUTING_PRIMARY_URL` | Valhalla URL (default: `http://valhalla.logistics.svc.cluster.local:8002`) |
| `ROUTING_CACHE_TTL` | Route cache TTL (default: `5m`) |
| `REDIS_ADDR` | Redis for routing cache and rate limiting |
| Webhook secrets | For outbound webhooks to ordering-backend |

---

## References

- [shared-docs/CROSS-SERVICE-DATA-OWNERSHIP.md](../../../shared-docs/CROSS-SERVICE-DATA-OWNERSHIP.md) — Canonical data ownership; logistics owns tasks, riders, fleets, PoD; ordering-backend stores only references.
- [Ordering-backend integrations](../../../ordering-service/ordering-backend/docs/integrations.md) — Logistics Service section documents REST, events, webhooks, and data ownership from ordering’s perspective.
- [Logistics API plan](../plan.md) — §2.5 Integration Points by Service.
- [Logistics API architecture](architecture.md) — Event architecture, RBAC, location tracking.
