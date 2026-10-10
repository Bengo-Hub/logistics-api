# Sprint 4 – Telemetry, Adherence & Safety (Weeks 8-9)

**Status**: Not Started  
**Start Date**: TBD  
**Target Completion**: TBD

## Goals

- Implement real-time location ingestion via WebSocket/gRPC with offline buffering.
- Add geofencing, route adherence detection, and deviation alerts.
- Build driver behavior ingestion (speeding, harsh braking/acceleration, idling) with basic scorecards.
- Create customer notification primitives and public tracking page MVP.

## Detailed Tasks

### 5.1 Real-Time Location Ingestion
- [ ] WebSocket endpoint:
  - `WS /v1/{tenant}/telemetry/stream` → accepts location updates from mobile app
  - Authentication: JWT token in query param or header
  - Message format: `{"fleet_member_id": "...", "lat": 0.0, "lng": 0.0, "speed_kph": 0.0, "bearing_deg": 0.0, "accuracy_m": 0.0, "timestamp": "..."}`
- [ ] gRPC streaming (optional):
  - `rpc StreamTelemetry(stream TelemetryPoint) returns (stream TelemetryAck)` → bidirectional stream
  - Higher throughput for fleet tracking
- [ ] Telemetry storage:
  - `telemetry_streams` table: session-level bundling (`fleet_member_id`, `device_id`, `started_at`, `ended_at`)
  - `telemetry_points` table: individual samples (compressed: store every Nth point, or aggregate by time window)
  - Partition by date for performance (PostgreSQL partitioning)
- [ ] Offline buffering:
  - Mobile app buffers points when offline
  - Batch upload on reconnect: `POST /v1/{tenant}/telemetry/batch` → accepts array of points
  - Deduplication: use `device_id` + `timestamp` as key

### 5.2 Geofencing & Adherence
- [x] Geofence definitions (`geo_fences`), done 2026-10-10: delivery and no-delivery (exclusion) areas, used by the delivery quote and task tagging (`docs/delivery-zones.md`)
- [ ] Geofence detection:
  - Background job: check `telemetry_points` against `geo_fences` using PostGIS `ST_Within`
  - Emit `geo_fence_events`: entry/exit events stored in `geo_fence_events` table
- [ ] Route adherence:
  - Compare actual route (from `telemetry_points`) to planned route (from `routes.route_plan_json`)
  - Calculate deviation: distance from planned polyline
  - Alert if deviation > threshold (configurable per tenant): `telemetry_alerts` table
- [ ] Deviation alerts:
  - `POST /v1/{tenant}/telemetry/alerts` → create alert (system or manual)
  - Types: `route_deviation`, `offline`, `speeding`, `unauthorized_stop`
  - Notify dispatcher via WebSocket or webhook

### 5.3 Driver Behavior Tracking
- [ ] Behavior event ingestion:
  - `POST /v1/{tenant}/telemetry/behavior` → accepts behavior events from mobile/telematics
  - Events: `speeding`, `harsh_braking`, `harsh_acceleration`, `cornering`, `idling`, `unauthorized_stop`
  - Store in `driver_behavior_events` table (see ERD, if exists) or `telemetry_alerts`
- [ ] Scorecard calculation:
  - Background job: aggregate behavior events per driver per day/week
  - Calculate scores: speeding incidents, harsh events count, idling duration
  - Store in `driver_scorecards` table (if exists) or aggregate on-demand
- [ ] Scorecard API:
  - `GET /v1/{tenant}/fleet-members/{id}/scorecard?period=week` → returns behavior metrics
  - Use for: driver performance reviews, route optimization (Sprint 9), ETA adjustments

### 5.4 Customer Notifications & Tracking
- [ ] Notification primitives:
  - Emit events to notifications service: `logistics.driver.arriving`, `logistics.task.completed`
  - Event payload: `task_id`, `customer_phone`, `eta_minutes`, `driver_name`
- [ ] Public tracking page:
  - `GET /v1/track/{tracking_code}` → public endpoint (no auth required)
  - `tracking_code` is generated when task is created (short UUID or alphanumeric)
  - Returns: task status, current location (if in_progress), ETA, driver info (optional)
- [ ] Tracking code generation:
  - Add `tracking_code` to `tasks` table (unique, indexed)
  - Generate on task creation: `uuid.New().String()[:8]` or custom format
- [ ] ETA updates:
  - Recalculate ETA when driver location updates
  - Push to customer via notifications service (if subscribed)
  - Store in `task_metadata.eta_updates[]` for audit

## Dependencies

- Sprint 3 complete (routes exist for adherence comparison)
- Notifications service (optional) for customer alerts
- Mobile app SDK (future) for location streaming

## Acceptance Criteria

- [ ] Location updates are ingested in real-time via WebSocket
- [ ] Geofence entry/exit events are detected and stored
- [ ] Route deviation alerts trigger when driver goes off-route
- [ ] Driver behavior events are captured and aggregated
- [ ] Scorecards are calculated accurately
- [ ] Public tracking page displays task status without authentication
- [ ] ETA updates are pushed to customers via notifications service

## Next Sprint Preview

Sprint 5 will integrate external signals (POS/inventory readiness, traffic incidents, weather) for route optimization and alternative route suggestions.

