# Sprint 1 – Fleet & Rider Management

**Status**: ✅ DONE (core complete; RBAC enforcement added post-sprint)
**Completed**: 2026-03-22

## Goals

- Fleet, vehicle, and fleet member CRUD
- Rider invite/approve/suspend/reject workflow
- KYC document upload (via media handler)
- Service-level RBAC: schemas, permission seeding, enforcement middleware
- 7-day stale application cleanup job

## Completed

### Fleet & Vehicle CRUD ✅
- Ent schemas: `fleet.go`, `vehicle.go` — tenant-scoped, status lifecycle
- `fleet.Service`: CreateFleet, GetFleet, ListFleets, CreateVehicle, AssignVehicle
- HTTP handlers under `/api/v1/{tenant}/fleet`:
  - `GET /fleet/` — get/list fleet
  - `GET/POST /fleet/members` — list, invite rider
  - `POST /fleet/members/batch` — batch invite
  - `GET /fleet/members/{id}` — member detail with KYC fields
  - `POST /fleet/members/{id}/approve|suspend|reject` — lifecycle actions
  - `DELETE /fleet/members/{id}` — remove member
  - `POST /fleet/members/{id}/vehicle` — assign vehicle
  - `POST /fleet/vehicles` — create vehicle

### RBAC ✅
- Ent schemas: `logistics_roles`, `logistics_permissions`, `user_role_assignments`
- Seed: `logistics.<module>.<action>` permissions auto-seeded on deploy
- `rbac.Service`: HasPermission, HasRole, AssignRole, RevokeRole, GetUserRoles
- `RequirePermission` middleware applied to all mutation routes
- Permission code constants in `rbac/models.go`

### KYC Document Upload ✅
- Riders upload docs via `POST /api/v1/{tenant}/media/upload` (stored to /data/media PVC)
- Admin reviews via `GET /fleet/members/{id}` → approve/reject

### Stale Application Cleanup ✅
- `fleet.Service.StartStaleRiderCleanup` goroutine: marks pending members older than
  7 days as `expired`, publishes `fleet.member_expired` event

### Rider Onboarding Fields ✅
- `onboarding_source`, `id_passport_attachment`, `vehicle_photo_attachment`,
  `selfie_attachment`, `additional_docs` on FleetMember (migration 20260401)

## Backlog

- Device registry (separate `devices` table) — deferred to Sprint 4 (telemetry)
- Dashboard stats endpoint — deferred to Sprint 3
- Vehicle documents table — not implemented; media upload covers the use case
