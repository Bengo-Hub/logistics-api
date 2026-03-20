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
| Valhalla | `http://valhalla.logistics.svc.cluster.local:8002` | `https://routing.codevertexitsolutions.com` |
| TileServer | `http://tileserver.logistics.svc.cluster.local:8080` | `https://tiles.codevertexitsolutions.com` |

### Rate Limiting (per tenant subscription plan)

| Feature | Starter | Growth | Professional |
|---------|---------|--------|--------------|
| `routing_requests_per_day` | 100 | 1,000 | 10,000 |
| `live_tracking_requests_per_day` | 500 | 5,000 | Unlimited |
| `live_tracking_duration_minutes` | 30 | 120 | Unlimited |
| `map_loads_per_day` | 200 | 2,000 | Unlimited |

When a limit is reached, the API returns `HTTP 429 Too Many Requests` with `X-RateLimit-*` headers and an upgrade URL.

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
