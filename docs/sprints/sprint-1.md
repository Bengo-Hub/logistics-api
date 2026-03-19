# Sprint 1 – Fleet & Rider Management (Weeks 2-3)

**Status**: ✅ DONE
**Start Date**: TBD (after Sprint 0 completion)
**Target Completion**: TBD

## Goals

- Implement CRUD operations for fleets, vehicles, and fleet members (riders/drivers). ✅ DONE
- Fleet & fleet member management ✅ DONE
- Establish service-level RBAC roles (dispatcher, rider, hub_operator) with role assignment.
- Build rider device registry and document upload/verification workflows.
- Create dispatcher console access scaffolding.

## Detailed Tasks

### 2.1 Fleet & Vehicle CRUD ✅ DONE
- [x] Ent schemas: ✅ DONE
  - `fleet.go` → `tenant_id`, `tenant_slug`, `name`, `type` (internal/third_party), `status`, `metadata`
  - `vehicle.go` → `tenant_id`, `fleet_id`, `vehicle_type` (bike/car/van), `make`, `model`, `license_plate`, `capacity_json`, `status`, `compliance_status`, `metadata`
  - `vehicle_documents.go` → `vehicle_id`, `document_type` (insurance/registration), `file_url`, `issued_at`, `expires_at`, `status`, `verified_by`, `verified_at`
- [x] Repository layer (`internal/modules/fleet/repository.go`): ✅ DONE
  - `CreateFleet(ctx, tenantID, fleet)` → persist fleet, validate tenant exists
  - `GetFleet(ctx, tenantID, fleetID)` → fetch with tenant isolation
  - `ListFleets(ctx, tenantID, filters)` → paginated list
  - `UpdateFleet(ctx, tenantID, fleetID, updates)` → partial update
  - `DeleteFleet(ctx, tenantID, fleetID)` → soft delete (set status=deleted)
- [x] Service layer (`internal/modules/fleet/service.go`): ✅ DONE
  - Business logic: validate fleet type, enforce tenant limits (subscription-based)
  - Vehicle assignment: link vehicles to fleets, validate capacity
- [x] HTTP handlers (`internal/modules/fleet/handlers.go`): ✅ DONE
  - `POST /v1/{tenant}/fleets` → create fleet (requires `logistics.fleet.create` scope)
  - `GET /v1/{tenant}/fleets` → list fleets (paginated)
  - `GET /v1/{tenant}/fleets/{id}` → get fleet details
  - `PUT /v1/{tenant}/fleets/{id}` → update fleet
  - `DELETE /v1/{tenant}/fleets/{id}` → soft delete
  - Similar endpoints for vehicles: `/v1/{tenant}/fleets/{fleetId}/vehicles`
- [x] OpenAPI annotations and contract updates ✅ DONE

### 2.2 Service-Level RBAC
- [ ] RBAC schema (`internal/ent/schema/rbac.go`):
  - `roles` table: `id`, `tenant_id`, `name` (dispatcher/rider/hub_operator), `permissions_json`, `created_at`
  - `role_assignments` table: `id`, `tenant_id`, `user_id` (ref to auth-service), `role_id`, `assigned_at`, `assigned_by`
- [ ] Role definitions (seed data):
  - `dispatcher`: `logistics.tasks.*`, `logistics.fleet.*`, `logistics.routes.*`
  - `rider`: `logistics.tasks.self.read`, `logistics.tasks.self.update`, `logistics.telemetry.self.create`
  - `hub_operator`: `logistics.tasks.hub.*`, `logistics.proof_of_delivery.create`
- [ ] Middleware (`internal/middleware/rbac.go`):
  - `RequireRole(role string)` → checks `role_assignments` table for current user
  - `RequirePermission(permission string)` → checks role permissions JSON
- [ ] Role assignment API:
  - `POST /v1/{tenant}/rbac/assignments` → assign role to user (requires admin scope)
  - `GET /v1/{tenant}/rbac/assignments` → list assignments
  - `DELETE /v1/{tenant}/rbac/assignments/{id}` → revoke role

### 2.3 Rider Device Registry & Documents
- [ ] Fleet member schema (`internal/ent/schema/fleet_member.go`):
  - `fleet_members` → `tenant_id`, `fleet_id`, `user_id` (ref to auth-service), `driver_code`, `status`, `vehicle_id`, `joined_at`, `suspended_at`, `metadata`
- [ ] Device registry (`internal/ent/schema/device.go`):
  - `devices` table: `id`, `tenant_id`, `fleet_member_id`, `device_type` (mobile/tablet/IoT), `device_id` (IMEI/UUID), `os_version`, `app_version`, `last_seen_at`, `status`
- [ ] Document upload handler:
  - `POST /v1/{tenant}/fleet-members/{id}/documents` → upload compliance docs (license, insurance)
  - Store files in S3-compatible storage or local filesystem (configurable)
  - Store metadata in `vehicle_documents` or `fleet_member_documents` table
- [ ] Document verification workflow:
  - Admin endpoint: `POST /v1/{tenant}/documents/{id}/verify` → mark as verified
  - Expiry checks: background job to alert on expiring documents
- [x] Fleet member CRUD: ✅ DONE
  - `POST /v1/{tenant}/fleets/{fleetId}/members` → add rider (requires `user_id` from auth-service)
  - `GET /v1/{tenant}/fleets/{fleetId}/members` → list members
  - `PUT /v1/{tenant}/fleet-members/{id}` → update status (active/suspended)
  - Device linking: `POST /v1/{tenant}/fleet-members/{id}/devices` → register device

### 2.4 Dispatcher Console Scaffolding
- [ ] Basic admin endpoints:
  - `GET /v1/{tenant}/dashboard/stats` → fleet count, active tasks, on-time %
  - `GET /v1/{tenant}/fleet-members/active` → list active riders with current location (from telemetry, Sprint 4)
- [ ] WebSocket endpoint (future):
  - `WS /v1/{tenant}/dispatcher/stream` → real-time task updates (Sprint 2+)
- [ ] Frontend scaffolding (optional, future):
  - React/Vue admin UI consuming REST APIs
  - Or document API for third-party dispatcher apps

## Dependencies

- Sprint 0 must be complete (auth integration, Ent schemas, CI/CD)
- Auth-service must expose user lookup API or discovery webhooks for `user_id` validation

## Acceptance Criteria

- [ ] Fleet CRUD operations work with tenant isolation
- [ ] Vehicles can be assigned to fleets with capacity validation
- [ ] RBAC roles are enforced on all endpoints
- [ ] Fleet members can be created with `user_id` references to auth-service
- [ ] Documents can be uploaded and verified
- [ ] All endpoints return proper HTTP status codes and error messages

## Next Sprint Preview

Sprint 2 will implement the task lifecycle (create, assign, complete) with finite state machine, SLA timers, and event auditing.

