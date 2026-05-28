# Sprint 3 – Routing & Dispatch (MVP) (Weeks 6-7)

**Status**: Partially Complete (Shift scheduling done; routing/dispatch pending)  
**Start Date**: 2026-05-28  
**Last Updated**: 2026-05-28

## ✅ Completed (2026-05-28)

| Task | File | Notes |
|------|------|-------|
| Shift management API | `internal/http/handlers/shift.go` | `GET/POST /{tenant}/shifts`, `PUT /{tenant}/shifts/{id}`, start/end actions |
| Shift scheduling registered in router | `internal/http/router/router.go` | ShiftHandler registered under tenant routes |
| RiderShift Ent schema | `internal/ent/schema/ridershift.go` | Pre-existing; now fully surfaced via API |
| Zone-based shift assignment | `shift.go` | Shifts accept `zone_ids[]` for zone-based dispatch |

## Goals

- Implement nearest-driver dispatch with geocoding and ETA calculation.
- Add routing constraints (capacity, time windows, driver shifts) with manual override.
- Build route templates, zones/territories, and initial simulation endpoints.
- Create provider abstraction for Google Maps and OSM/OSRM with runtime selection.

## Detailed Tasks

### 4.1 Nearest-Driver Dispatch & Geocoding
- [ ] Geocoding service (`internal/modules/routing/geocoding.go`):
  - Provider abstraction: Google Maps Geocoding API or OpenStreetMap Nominatim
  - `GeocodeAddress(ctx, address)` → returns `geo_point` (lat/lng)
  - Cache results in Redis (TTL: 30 days) to reduce API calls
- [ ] Nearest driver algorithm:
  - Query active fleet members with current location (from telemetry, Sprint 4) or last known location
  - Calculate distance using Haversine or PostGIS `ST_Distance`
  - Filter by: capacity match, shift active, compliance status
  - Return sorted list: `GET /v1/{tenant}/tasks/{id}/available-drivers?limit=10`
- [ ] ETA calculation:
  - Use routing provider (Google/OSRM) to get distance + duration
  - Add service time buffer (configurable per task type)
  - Return: `estimated_distance_meters`, `estimated_duration_seconds`, `eta_at_destination`
- [ ] Dispatch endpoint:
  - `POST /v1/{tenant}/tasks/{id}/dispatch` → auto-assign to nearest available driver
  - Optional: `preferred_driver_id` to override auto-selection

### 4.2 Routing Constraints
- [ ] Capacity validation:
  - Check vehicle `capacity_json` (weight/volume/dimensions) against task `metadata.items[]`
  - Reject assignment if capacity exceeded
- [ ] Time window validation:
  - `requested_pickup_at` and `requested_dropoff_at` must be within driver shift (`rider_shifts`)
  - Check driver availability: no overlapping tasks
- [ ] Shift management:
  - `rider_shifts` table: `fleet_member_id`, `shift_start`, `shift_end`, `status`
  - API: `GET /v1/{tenant}/fleet-members/{id}/shifts` → list shifts
  - Validate: task assignment only if shift is active
- [ ] Manual override console:
  - `POST /v1/{tenant}/tasks/{id}/assign` → dispatcher can manually assign (bypasses nearest-driver)
  - Requires `logistics.tasks.override` permission

### 4.3 Route Templates & Zones
- [ ] Route templates (`internal/ent/schema/route_template.go`):
  - `route_templates` table: `tenant_id`, `name`, `zone_id`, `route_plan_json`, `created_at`
  - Store common routes (e.g., "Morning CBD Route") for reuse
- [ ] Zones/territories (`internal/ent/schema/zone.go`):
  - `zones` table: `id`, `tenant_id`, `name`, `geometry` (PostGIS Polygon), `metadata`
  - Link tasks to zones: `tasks.zone_id` (optional)
  - Zone-based dispatch: prefer drivers assigned to zone
- [ ] Zone API:
  - `POST /v1/{tenant}/zones` → create zone (polygon from GeoJSON)
  - `GET /v1/{tenant}/zones` → list zones
  - `GET /v1/{tenant}/zones/{id}/tasks` → tasks in zone
- [ ] Route planning:
  - `POST /v1/{tenant}/routes/plan` → generate route for multiple tasks
  - Input: `task_ids[]`, `optimization_mode` (distance/time/cost)
  - Output: `route_plan_json` (ordered task sequence, waypoints, ETAs)
  - Store in `routes` table (see ERD)

### 4.4 Provider Abstraction
- [ ] Provider interface (`internal/modules/routing/provider.go`):
  ```go
  type RoutingProvider interface {
    Geocode(ctx, address) (GeoPoint, error)
    Route(ctx, origin, destination, waypoints[]) (Route, error)
    DistanceMatrix(ctx, origins[], destinations[]) (Matrix, error)
  }
  ```
- [ ] Google Maps implementation:
  - Use Google Maps Routes API (Directions, Distance Matrix)
  - Handle traffic-aware ETAs (if licensed)
  - Rate limiting: respect quota (see Sprint 0 provider registry)
- [ ] OSM/OSRM implementation:
  - Use OSRM HTTP API (self-hosted or public instance)
  - Fallback when Google quota exceeded
  - Static routing (no real-time traffic)
- [ ] Provider selection:
  - Config per tenant: `provider_configs.routing_provider` (google/osrm/auto)
  - Auto mode: try Google first, fallback to OSRM on error
- [ ] Route storage:
  - `routes` table: `task_id`, `route_plan_json`, `distance_meters`, `duration_seconds`, `routing_provider`, `generated_at`
  - `route_segments` table: granular steps with polylines

## Dependencies

- Sprint 2 complete (tasks exist for routing)
- Provider credentials configured (Sprint 0)
- PostGIS extension enabled in PostgreSQL

## Acceptance Criteria

- [ ] Geocoding converts addresses to coordinates
- [ ] Nearest-driver algorithm returns valid assignments
- [ ] ETA calculations are accurate (±10% of actual)
- [ ] Capacity and time window constraints are enforced
- [ ] Manual override works for dispatchers
- [ ] Route templates can be created and reused
- [ ] Provider abstraction allows switching between Google/OSRM

## Next Sprint Preview

Sprint 4 will add real-time telemetry (location streaming, geofencing, route adherence) and driver behavior tracking.

