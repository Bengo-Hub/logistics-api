## Logistics Service Delivery Plan

### Vision & Mission
- Orchestrate all mid-mile and last-mile logistics for BengoBox properties (food delivery, ecommerce, POS backoffice, inventory transfers) using a single, multi-tenant platform that shares the core `tenant_slug` and outlet registry with every Go microservice.
- Provide real-time visibility over riders/drivers, fleets, tasks, and service-level agreements while integrating tightly with inventory, POS, delivery apps, and treasury.
- Support a wide spectrum of flows: on-demand delivery, scheduled routes, batch consolidation, courier marketplace, third-party carrier integrations, and reverse logistics (returns, waste recovery).
- Offer subscription-aware features (advanced routing, marketplace integrations) aligned with tenant licensing.

### 1. Full Requirements
1. Functional modules:
  - Tenant & Fleet Management: tenants, fleets, rider/driver profiles, vehicles, capacity, compliance docs; shared `tenant_slug` and outlet IDs; tenant/outlet discovery webhook backfill.
  - Task Lifecycle: intake (cafe orders, inventory, POS, returns); FSM (created → assigned → en‑route → completed/failed); multi‑leg; SLAs; escalations; auditing.
  - Routing & Dispatch: on‑demand nearest driver; scheduled batches/waves; zone dispatch; optimization (TSP/VRP/VRPTW, pickup–delivery precedence, multi‑depot/multi‑day/split); manual override.
  - Real‑time Tracking & Telemetry: device/SDK/IoT location streaming; geofencing; route adherence; live ETAs and deviation alerts.
  - Proof of Delivery & Reverse Logistics: codes/signatures/photos/NFC, temp logs; returns/exchanges; audit trail.
  - Driver Behavior & Safety: ingest speeding/harsh/cornering/idling/unauthorised stops; driver scorecards; geofenced advisories; safety thresholds per tenant.
  - Expenses & Finance Hooks: capture fuel/toll/parking/maintenance/penalties per route/stop/vehicle; attach receipts; map categories to GL accounts; emit Expense/Bill/Journal to `treasury-app` with dimensions (tenant, route, vehicle, driver, cost center); support mileage reimbursements and inclusion in driver payouts.
  - Analytics & Intelligence: KPIs (on‑time %, distance, utilisation); heat maps; AI‑driven constraints (lateness/dwell/no‑show) folded into routing; what‑if simulation.
  - Subscription & Gating: plan‑based access (advanced routing/connectors/analytics); usage tracking (tasks, km).
  - Cross‑cutting: auth SSO + service‑level RBAC; multi‑tenancy; resilience; offline; compliance; observability.
-Route optimization details:
  - Inputs: time windows, service times, shifts/breaks, capacities/compartments/cold chain, skills/vehicle type, traffic‑aware ETAs, restrictions/LEZ, appointments, building access, cost models (fixed/variable, overtime/tolls/surge).
  - Objectives: minimize lateness/time/distance/cost; balance workload; maximize SLA.
  - Artifacts & safeguards: templates, zones/territories, waves, hubs/cross‑dock; adherence detection; exceptions; approvals.

### 2. System Analysis (Architecture, Integrations, Design)
- Architecture:
  - Gateway/API (REST + gRPC streaming), Dispatch Engine, Routing Service (OSRM/Maps), Telemetry Stream, Workflows/Saga, Notification Adapter, Integration Layer; PostgreSQL + Redis; Ent migrations; outbox to NATS/Kafka; OTel + Prometheus/Grafana; Docker/Helm/ArgoCD.
  - **Auth-Service SSO Integration:** ✅ **COMPLETED** - Complete Go service implementation with `shared/auth-client` v0.1.0 library for production-ready JWT validation using JWKS from auth-service. All protected `/v1/{tenant}` routes require valid Bearer tokens. OpenAPI 3.0 spec updated with BearerAuth security scheme. Service scaffolding complete with HTTP server, configuration, logging, health endpoints, middleware, and infrastructure (PostgreSQL, Redis, NATS). **Deployment:** Uses monorepo `replace` directives with versioned dependency (`v0.1.0`). Go workspace (`go.work`) handles local development automatically. Each service has independent DevOps workflows and can be deployed separately while sharing the auth library. See `shared/auth-client/DEPLOYMENT.md` and `shared/auth-client/TAGGING.md` for details. **DevOps alignment:** logistics-api, logistics-ui, and rider-app use the same pattern as auth-api/notifications-api: sync-secrets job, verify-secrets step, centralized devops-k8s (apps/logistics-api, apps/logistics-ui, apps/rider-app), GIT_COMMIT_ID image tags, and centralized update_helm_values script.
- API surface:
  - `/v1/{tenant}/tasks|routes|fleet|carriers|proof`; bulk CSV import/export; public tracking `/v1/track/{code}`; webhooks (`logistics.task.*`, `logistics.rider.offline`, `logistics.carrier.failure`); provider registry `/integrations/providers` with encrypted configs; `/locks/access-windows|events`; `/incidents`; `/weather/forecasts`; safety `/telemetry/driver-events` and driver scorecards; route alternatives endpoints.
- Provider configuration & secrets:
  - Per‑tenant configs encrypted at rest (envelope/KMS); redacted reads; rotation/audits; precedence: env defaults → tenant overrides → feature flags.
- Integration context (capabilities & how):
  - Maps & routing: Google Maps Routes (traffic‑aware ETAs, restrictions/tolls), Roads Speed Limits (speeding context), Traffic incidents (where licensed) for accident/closure/hazard overlays and alt‑route prompts; OSM/OSRM for open routing (static traffic) with incident overlays from external feeds when needed.
  - Weather: OpenWeather One Call and/or Tomorrow.io for hyperlocal forecasts/alerts; open alternatives (Open‑Meteo, NOAA/NWS CAP). Weather adjusts ETAs and triggers alt‑route suggestions and advisories.
  - Smart locks: August/Yale cloud APIs (OAuth + webhooks), Nuki Web/Bridge API, and supported Schlage integrations; delivery unlock at destination by designated user (code/biometric/device) → PoD event + audit; manual override workflow.
  - Telematics: Samsara/Geotab APIs for driver events and vehicle diagnostics; scheduled ingestion with dedupe/backoff; enrich driver scorecards and ETA models.
  - Internal microservices: Treasury for payouts/surcharges/expenses; Inventory/POS/Cafe Backend for readiness/transfer hooks and state sync; Notifications for customer ETA and SLA alerts.
- Data model summary:
  - Tasks/Routes/Fleet/Telemetry/PoD/Integrations/Optimization/Safety/Access/Governance as described; include expenses linkage fields (route_id, vehicle_id, driver_id, category, receipt_ref) in events exported to treasury.

#### 2.5 Integration Points by Service
- Inventory Service:
  - Webhooks: `inventory.transfer.created`, `inventory.transfer.completed`
  - REST: `/v1/{tenant}/inventory/availability?zone={zone}&branch={branch}` for zone/branch-aware availability to inform dispatch
- POS Service:
  - Webhooks: `pos.order.ready`, `pos.order.handoff`
  - REST: `/v1/{tenant}/pos/orders/{id}/handoff` for driver pickup confirmation
- Treasury App:
  - REST: `/v1/{tenant}/expenses`, `/v1/{tenant}/bills`, `/v1/{tenant}/journals`, `/v1/{tenant}/payouts`
  - Events: `treasury.payout.completed`, `treasury.expense.approved`
- Notifications App:
  - Webhooks: `customer.eta.updated`, `customer.delivery.failed`, `ops.sla.alert`
  - REST: `/v1/{tenant}/notify` with channels (sms,email,push,voice-masked)
- Cafe Backend:
  - Webhooks: `cafe.order.created`, `cafe.order.ready` → triggers task creation
  - REST: `POST /v1/{tenant}/tasks` → cafe backend creates delivery tasks
  - Events: `logistics.task.assigned`, `logistics.task.completed`, `logistics.route.updated`
  - Streaming: ETA feed for customer app subscriptions via WebSocket/SSE

### 4. Delivery Roadmap

Each sprint is documented in detail with tasks, dependencies, acceptance criteria, and progress tracking. See individual sprint files for comprehensive breakdowns.

1. **[Sprint 0 – Platform Foundations](docs/sprints/sprint-0.md)** (Week 1)
   - Repository scaffolding, logging, health/liveness endpoints
   - Ent base schemas (tenants, outlets, fleets, fleet_members)
   - Auth-service SSO integration (JWT middleware, OAuth2, scope guards)
   - CI/CD pipeline, Helm charts, secrets management
   - Provider registry and encrypted-at-rest secrets primitives

2. **[Sprint 1 – Fleet & Rider Management](docs/sprints/sprint-1.md)** (Weeks 2-3)
   - Fleet and vehicle CRUD operations
   - Service-level RBAC (dispatcher, rider, hub_operator)
   - Rider device registry and document upload/verification
   - Dispatcher console access scaffolding

3. **[Sprint 2 – Task Lifecycle](docs/sprints/sprint-2.md)** (Weeks 4-5)
   - Task entities (tasks, steps, events, assignments) with finite state machine
   - Create/assign/complete flows with auditing and idempotency
   - SLA timers, escalation rules, and exception taxonomy

4. **[Sprint 3 – Routing & Dispatch (MVP)](docs/sprints/sprint-3.md)** (Weeks 6-7)
   - Nearest-driver dispatch with geocoding and ETA calculation
   - Routing constraints (capacity, time windows, driver shifts)
   - Route templates, zones/territories, and simulation endpoints
   - Provider abstraction (Google Maps + OSM/OSRM runtime selection)

5. **[Sprint 4 – Telemetry, Adherence & Safety](docs/sprints/sprint-4.md)** (Weeks 8-9)
   - Real-time location ingestion (WebSocket/gRPC) with offline buffering
   - Geofencing, route adherence detection, and deviation alerts
   - Driver behavior tracking (speeding, harsh braking/acceleration, idling) with scorecards
   - Customer notification primitives and public tracking page MVP

6. **[Sprint 5 – External Signals & Alt-Routes](docs/sprints/sprint-5.md)** (Weeks 10-11)
   - POS/inventory/cafe webhook integration (readiness, transfers)
   - Traffic incident ingestion (accidents, closures, hazards) with alternative route prompts
   - Weather forecast integration for ETA/routing penalties and advisories
   - Bulk import/export and webhook signing/retries hardening

7. **[Sprint 6 – Connectors (Carriers & Smart Locks)](docs/sprints/sprint-6.md)** (Weeks 12-13)
   - Generic connector framework for third-party carriers
   - Smart lock integrations (August, Yale, Nuki, Schlage) with access windows and PoD enforcement
   - Rate shopping, label/manifest generation, and cost capture

8. **[Sprint 7 – Billing, Expenses & Subscription](docs/sprints/sprint-7.md)** (Weeks 14-15)
   - Usage metrics (tasks, km) with plan gating for advanced features
   - Tariff system (base fare, per-km, per-minute, surcharges) with payout calculations
   - Expense export to treasury (fuel/toll/parking/maintenance) with GL mapping and cost centers
   - Receipt upload and mileage reimbursement support

9. **[Sprint 8 – Reverse Logistics & Compliance](docs/sprints/sprint-8.md)** (Weeks 16-17)
   - Returns/exchanges workflows with hub flows
   - PoD upgrades (photos, NFC support)
   - Data retention policies, GDPR export/delete endpoints, and compliance reporting
   - Safety features (no-go zones, HAZMAT skills) with approval workflows

10. **[Sprint 9 – Analytics & AI Constraints](docs/sprints/sprint-9.md)** (Weeks 18-19)
    - Dashboards for on-time %, distance, utilization, SLA alerting, and capacity planning
    - Simulation UX for what-if scenarios
    - Lateness/dwell models integrating driver/incident/weather features into constraints engine
    - Route performance tuning with matrix/ETA caching

11. **[Sprint 10 – Launch & Support](docs/sprints/sprint-10.md)** (Weeks 20-21)
    - Production runbooks, SLOs/alerts, and on-call procedures
    - Comprehensive documentation and training materials
    - Tenant onboarding playbooks and migration/backfill scripts
    - Backlog triage and production polish

### 5. Provider Configuration & Secrets (Details)
- Per-tenant provider registry with the following categories:
  - Mapping & Routing: Google Maps (geocoding, traffic/incidents), OSM/OSRM (routing, tiles), Maptiler.
  - Weather & Hazards: forecast and alert providers (configurable).
  - Smart Locks: supported vendors with API credentials and capabilities metadata.
- All provider credentials are encrypted at rest with envelope encryption and redacted in API responses.
- Configuration schema includes: API keys, endpoints, toggle flags, cost preferences, rate limits, and fallback order.
- Audit trails for create/update/read of provider settings; periodic rotation jobs with alerting on failure.

### 6. Incidents & Weather in Optimization
- Incident and weather feeds adjust ETA matrices and edge weights dynamically.
- Hard constraints: closed roads/hard closures become no-go edges; soft constraints penalize heavy traffic/hazards.
- Dispatch reacts to events: re-insert stops, swap riders, or re-sequence routes to preserve SLAs.

### 7. Future Backlog & Enhancements
- Machine learning for demand forecasting, dispatch optimization, and dynamic pricing.
- Incentive engine for riders (gamification, bonuses).
- Drone/robot delivery connectors, EV fleet energy planning.
- IoT integrations (temperature sensors, door sensors) with alerts.
- Carbon footprint tracking, sustainability metrics.
- Marketplace API for external partners to plug-in tasks or capacity dynamically.

### 8. RBAC and seed (MVP)
- **Current:** No local Role/Permission model; RBAC is delegated to **auth-api JWT**. The API validates Bearer tokens and enforces tenant/user context from auth-api claims.
- **When Ent + seed exist:** Seed script MUST create permissions for resources **tasks**, **riders**, **fleet** with the MVP action set (add, read, read_own, change, change_own, delete, manage, manage_own) and seed core data (tenants, default fleet/config). Align with auth-api and ordering-backend seed patterns; see shared-docs/mvp-critical-path.md §8.

### 9. Immediate Next Steps
- Confirm ERD alignment with inventory/POS (shared concepts: warehouse, order IDs).
- Define contract-first API specs for cafe-backend and POS interactions.
- Validate mapping provider contracts, start reference implementation (Mapbox + OSRM fallback).
- Produce threat model (location spoofing, assignment fraud) and commence Sprint 0 after stakeholder approval.

### 10. Glossary & Acronyms (Plain‑English Reference)
- API (Application Programming Interface): A defined way for software systems to communicate.
- REST (Representational State Transfer): A simple web API style using URLs and HTTP methods (GET/POST/PUT/DELETE).
- gRPC (Google Remote Procedure Call): A high‑performance binary protocol over HTTP/2 for service‑to‑service calls.
- OpenAPI/Swagger: A machine‑readable contract that describes REST APIs for tooling, documentation, and code generation.
- Webhook: An HTTP callback a system sends to another system to notify about an event (for example, “delivery completed”), typically signed to verify authenticity.
- JSON/Protobuf: Data formats; JSON is human‑readable, Protobuf is a compact binary format used by gRPC.
- PostgreSQL (Postgres): Relational database for structured data; PostGIS is a spatial extension for geospatial queries.
- Redis: In‑memory data store for caching, queues, and rate limiting.
- NATS JetStream / Apache Kafka: Message/streaming systems used to move events reliably between services.
- OSRM (Open Source Routing Machine): Open‑source routing engine using OpenStreetMap road data.
- OpenStreetMap (OSM): Community‑maintained map data set; used for geocoding and routing.
- Google Maps Platform: Commercial mapping APIs (geocoding, directions/routes, traffic, incidents, speed limits).
- KEDA (Kubernetes Event‑Driven Autoscaling): Scales workloads based on queue depth or external metrics.
- HPA/VPA: Horizontal/Vertical Pod Autoscaler for scaling containerized apps in Kubernetes.
- Kubernetes (K8s), Helm, Argo CD: Orchestration platform; Helm packages applications; Argo CD GitOps‑deploys them.
- OpenTelemetry (OTel), Prometheus, Grafana: Observability stack for traces/metrics and dashboards.
- RBAC (Role‑Based Access Control): Granting user/system permissions by role. SSO (Single Sign‑On) centralizes authentication.
- KMS/HSM: Key management and hardware security modules for secure key storage and cryptographic operations.
- SLA (Service‑Level Agreement): Target service metrics (for example, delivery on time). KPI (Key Performance Indicator): Measurable success metric.
- AI/ML: Automated learning models used here to predict delays (lateness risk) and optimize routes.