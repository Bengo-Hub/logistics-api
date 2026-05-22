# Changelog

This file keeps track of significant changes to the Logistics Service.  
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and [SemVer](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added (2026-05-22)
- **Telemetry service**: GPS location ingestion (`POST /telemetry/location`), stream
  management (`POST /telemetry/stream/end`), and query endpoints. Auto-creates
  TelemetryStream per rider; updates Redis GEO for dispatch nearest-rider queries.
- **Earnings REST API**: `GET/POST /earnings/pricing-rules`, `GET /earnings/statements`,
  `POST /earnings/statements/generate`, `GET /earnings/events` (audit trail),
  `GET /riders/me/earnings` and `GET /riders/me/earnings/statements` for rider self-service.
- **Dispatch endpoint**: `POST /tasks/{id}/dispatch` — manually trigger auto-dispatcher
  for an unassigned task.
- **Proof of Delivery GET**: `GET /tasks/{id}/pod` — retrieve existing PoD record.
- **SSE task streaming**: `GET /tasks/{id}/stream` — Server-Sent Events endpoint;
  broadcasts `status_changed` events to logistics-ui and public tracker in real time.
- **ETA on status change**: `UpdateStatus` now triggers `ETAUpdater.ComputeAndPublishETA`
  on `accepted` and `en_route` transitions (goroutine, non-blocking).
- **SLA monitor**: `SLAMonitor` goroutine scans overdue tasks every 5 minutes, publishes
  `logistics.task.sla_breached` NATS events. `EscalationLevel()` classifies breach severity.
- **RBAC enforcement middleware**: `RequirePermission` middleware applied to all task,
  fleet, and zone mutation routes. Permission code constants in `rbac/models.go`.
- **outlet_id on tasks**: new `outlet_id` field supports multi-outlet filtering via
  `X-Outlet-ID` header (Atlas migration `20260521165641`).
- **Atlas baseline**: `baselineAtlasIfNeeded` in `cmd/migrate/main.go` transitions
  existing auto-migrated DBs to Atlas versioned migrations on first deploy without
  re-applying already-applied SQL.

### Fixed (2026-05-22)
- **Migration crash (BUG-001)**: `migrations.go` now tries LocalDir first then falls back
  to embedded FS, fixing the Docker container path issue that caused 145 crash-loop
  restarts on rev 29.

### Changed
- Standardized API base path to `/api/v1`
- Auth-service SSO integrated via `shared-auth-client` (JWKS validation)
- All protected routes require valid Bearer JWT tokens
- Swagger UI at `/v1/docs`

## [2025-01-17] Sprint 0 Documentation & Organization
- Reorganized `plan.md` with numbered sections, full requirements, system analysis, and integration points.
- Updated `plan.md` to link to individual sprint files instead of duplicating task lists, improving maintainability.
- Enhanced ERD (`docs/erd.md`) with comprehensive cross-service entity alignment section:
  - Entity ownership rules (which service owns which entities)
  - Integration patterns (tenant discovery, user identity, inventory transfers, zone dispatch, financial events)
  - ID & reference conventions
  - Zone & dispatch collaboration details
- Deleted duplicate `docs/erd-alignment.md` file (content merged into `docs/erd.md`).
- Created comprehensive sprint documentation files for all 11 sprints:
  - `docs/sprints/sprint-0.md` through `docs/sprints/sprint-10.md`
  - Each sprint file includes: goals, detailed tasks, dependencies, acceptance criteria, progress log, and next sprint preview
- Added API contract skeleton: `docs/api/openapi.yaml` (tasks, routes, provider configs).
- Authored mapping provider integration guide: `docs/integrations/mapping-providers.md` (Google Maps, OSRM/OSM, Maptiler).
- Drafted initial Threat Model: `docs/threat-model.md` (STRIDE, mitigations).
- Updated `README.md` with links to new documentation.
