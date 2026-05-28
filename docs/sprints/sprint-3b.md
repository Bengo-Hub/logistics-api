# Sprint 3B – Multi-Use-Case Revamp & KEMSA Distribution (2026-05-28)

**Status**: Complete  
**Completed**: 2026-05-28  
**Services**: logistics-api, logistics-ui, rider-app  
**Commits**: `b1a88e7` (api), `f77ddfe` (ui), `126a331` (rider-app)

---

## Context

The logistics service was initially built for a single generic delivery/fleet use case. This sprint expanded it to serve **three distinct business use cases** with appropriate module gating, a new distribution module for KEMSA/medical supply chain, and surfacing of previously schema-only features.

---

## Completed Work

### Backend (logistics-api)

#### Module Enablement System
- **`internal/consts/modules.go`** — Module key constants + `UseCaseModules` map for courier/delivery/distribution defaults
- **`internal/http/handlers/identity.go`** — `/auth/me` now returns `enabled_modules` and `use_case`
- **`internal/http/handlers/config_handler.go`** — Exposes `logistics.enabled_modules` config key for per-tenant module override

#### KEMSA / Distribution Entities (Ent + Atlas)
- **`internal/ent/schema/shipment.go`** — Shipment entity: batch of tasks, cold-chain params, seal_number, dispatch timestamps
- **`internal/ent/schema/chain_of_custody.go`** — ChainOfCustody: immutable custody event ledger per shipment
- **`internal/ent/schema/task.go`** — Added `shipment_id` (FK) and `seal_number` fields
- **`internal/ent/schema/telemetrypoint.go`** — Added `temperature_celsius` for cold-chain IoT monitoring
- **`internal/ent/schema/proofofdelivery.go`** — Added hospital PoD fields: `receiving_staff_name`, `receiving_staff_signature_url`, `condition_on_arrival`, `received_quantity`, `batch_reference`
- **`internal/ent/migrate/migrations/20260528180000_shipment_chain_cold_chain.sql`** — Atlas migration

#### New API Handlers
- **`internal/http/handlers/shipment.go`** — Shipment CRUD, dispatch, chain-of-custody endpoints
- **`internal/http/handlers/shift.go`** — Rider shift scheduling CRUD (GET/POST/PUT/start/end)
- **`internal/http/handlers/analytics.go`** — `GET /{tenant}/analytics/kpis?period=today|7d|30d`
- **`internal/http/handlers/fleet_dto.go`** — `FleetMemberResponse` DTO flattening User edge (first_name/last_name/email/phone from User)

#### Router & App Wiring
- **`internal/http/router/router.go`** — Registered analytics, shipment, shift handlers
- **`internal/app/app.go`** — Wired new handlers with DI

#### Dependencies
- `go.mod` — Upgraded `shared-events` v0.2.0 → v0.3.0

---

### Frontend (logistics-ui)

#### Module Gating
- **`src/hooks/use-module-access.ts`** — `useModuleAccess()` hook: `hasModule()`, `isPlatformOwner`, use-case flags
- **`src/components/ui/module-gate.tsx`** — `<ModuleGate moduleKey="...">` component
- **`src/components/sidebar.tsx`** — Module-gated nav items; sidebar dark-theme CSS vars

#### New Pages
- **`src/app/[orgSlug]/distribution/page.tsx`** — KEMSA shipment list, create wizard, chain-of-custody timeline (gated by `distribution` module)
- **`src/app/[orgSlug]/shifts/page.tsx`** — Rider shift scheduling calendar/list (gated by `fleet` module)
- **`src/app/[orgSlug]/reporting/page.tsx`** — KPI delivery summary + rider performance table + CSV export

#### Updated Pages
- **`src/app/[orgSlug]/analytics/page.tsx`** — Real KPI data from `/analytics/kpis`; fleet utilization + on-time rate cards
- **`src/app/[orgSlug]/vehicles/page.tsx`** — License plate replaces registration_number; removed year/capacity_kg
- **`src/app/[orgSlug]/riders/[id]/page.tsx`** — Vehicle fields aligned to backend schema
- **`src/app/[orgSlug]/settings/page.tsx`** — Modules management tab
- **`src/app/[orgSlug]/platform/page.tsx`** — Fixed `isSuperUser` → `isPlatformOwner`
- **`src/app/[orgSlug]/tracking/page.tsx`** — Fixed `session.access_token` → `session.accessToken`

#### New Hooks & API
- **`src/hooks/use-analytics.ts`** — `useKPIs(period)` hook
- **`src/hooks/use-shipments.ts`** — Shipment CRUD hooks
- **`src/hooks/use-shifts.ts`** — Shift CRUD hooks
- **`src/lib/api/logistics.ts`** — `KPIResponse` type + `fetchKPIs`, shipment, shift API functions

#### Type Fixes
- **`src/types/logistics.ts`** — `Vehicle` type aligned to backend schema; `FleetMember` updated with rating/joined_at fields; `ServiceAuthMe` extended with `enabled_modules` + `use_case`

---

### Rider App

- **`src/app/[orgSlug]/profile/page.tsx`** — "Your Performance" scorecard for active riders (avg rating, total deliveries, specialization tags)
- **`src/hooks/useRiderProfile.ts`** — Fixed endpoint to `GET /{tenant}/riders/me` (was using wrong path)
- **`src/types/logistics.ts`** — `FleetMember` updated with `average_rating`, `total_ratings`, `specialization_tags`, vehicle edge; added `RiderMeResponse` type

---

## Module Inventory (as of 2026-05-28)

| Module Key | Description | Default Use Cases |
|------------|-------------|------------------|
| `dashboard` | Main dashboard | All |
| `fleet` | Fleet member + shift management | All |
| `dispatch` | Task dispatch + assignment | All |
| `tracking` | Live tracking + GPS | All |
| `analytics` | KPI analytics + charts | All |
| `earnings` | Rider earnings statements | All |
| `vehicles` | Vehicle management | courier, distribution |
| `pricing` | Surge pricing UI | delivery |
| `distribution` | Shipment + chain-of-custody (KEMSA) | distribution only |
| `cold_chain` | Cold chain SLA monitoring | distribution only |
| `smart_locks` | Seal/lock integrations | distribution only |
| `reporting` | Reports + CSV/PDF export | All |
| `settings` | Service configuration | All |
| `rbac` | Role & permission management | All |

---

## What Remains

| Area | Description | Sprint |
|------|-------------|--------|
| Cold-chain SLA monitor | Poll telemetry, alert on temperature breach | Sprint 4 |
| Smart lock OAuth (August/Yale) | Full OAuth integration; currently only `seal_number` captured | Sprint 6 |
| Report PDF generation | Backend PDF templates for shipment manifest / custody ledger | Sprint 7 |
| Surge pricing UI | Delivery marketplace pricing page | Sprint 5 |
| Route optimization | TSP/VRP for batch task assignment | Sprint 3 |
| Public tracking page | Verify `GET /track/{code}` end-to-end | Sprint 2 |
