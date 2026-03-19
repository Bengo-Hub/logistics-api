# Sprint 2 – Task Lifecycle (Weeks 4-5)

**Status**: Not Started  
**Start Date**: TBD  
**Target Completion**: TBD

## Goals

- Implement task lifecycle entities (tasks, steps, events, assignments) with finite state machine.
- Build create/assign/complete flows with auditing and idempotency.
- Add SLA timers, escalation rules, and exception taxonomy.

## Detailed Tasks

### 3.1 Task Entities & FSM
- [ ] Ent schemas:
  - `task.go` → `id`, `tenant_id`, `external_reference` (order ID from ordering-backend/POS), `source_service`, `task_type` (delivery/pickup/transfer/return), `priority`, `status` (created/assigned/in_progress/completed/failed), `sla_due_at`, `requested_pickup_at`, `requested_dropoff_at`, `metadata` (JSON: SKUs, quantities, special instructions)
  - `task_step.go` → `id`, `task_id`, `step_type` (pickup/dropoff), `sequence`, `location_name`, `address_json`, `geo_point` (PostGIS), `contact_name`, `contact_phone`, `requires_signature`, `requires_photo`, `metadata`
  - `task_event.go` → `id`, `task_id`, `event_type`, `actor_id`, `actor_type` (user/system), `payload` (JSON), `occurred_at`
  - `task_assignment.go` → `id`, `task_id`, `fleet_member_id`, `assignment_status` (pending/accepted/declined/completed), `assigned_at`, `accepted_at`, `declined_at`, `completed_at`, `reason_code`, `metadata`
- [ ] Finite State Machine (`internal/modules/tasks/fsm.go`):
  - State transitions: `created` → `assigned` → `in_progress` → `completed` / `failed`
  - Validation: ensure valid transitions (e.g., can't complete from `created`)
  - Emit `task_events` on each transition
- [ ] Task service (`internal/modules/tasks/service.go`):
  - `CreateTask(ctx, tenantID, task)` → validate tenant, create task + initial event
  - `AssignTask(ctx, tenantID, taskID, fleetMemberID)` → create assignment, transition to `assigned`, emit event
  - `AcceptTask(ctx, tenantID, taskID, fleetMemberID)` → update assignment, transition to `in_progress`
  - `CompleteTask(ctx, tenantID, taskID, proofOfDelivery)` → transition to `completed`, emit event, trigger billing (Sprint 7)
  - `FailTask(ctx, tenantID, taskID, reason)` → transition to `failed`, emit event, trigger escalation if SLA breached

### 3.2 Create/Assign/Complete Flows
- [ ] Task creation API:
  - `POST /v1/{tenant}/tasks` → create task from external order (ordering-backend/POS/inventory)
  - Request body: `external_reference`, `source_service`, `task_type`, `steps[]`, `metadata`
  - Validate: tenant exists, steps have valid addresses (geocode if needed, Sprint 3)
  - Idempotency: use `external_reference` + `source_service` as idempotency key (return existing task if duplicate)
- [ ] Assignment flow:
  - `POST /v1/{tenant}/tasks/{id}/assign` → assign to fleet member (dispatcher action)
  - `POST /v1/{tenant}/tasks/{id}/accept` → rider accepts task (mobile app)
  - `POST /v1/{tenant}/tasks/{id}/decline` → rider declines (with reason)
- [ ] Completion flow:
  - `POST /v1/{tenant}/tasks/{id}/complete` → mark complete with PoD (signature/photo/OTP)
  - Validate: task is `in_progress`, PoD provided if required
  - Emit `logistics.task.completed` event to outbox (consumed by ordering-backend/POS)
- [ ] Auditing:
  - All state changes logged in `task_events` with actor (user ID from JWT)
  - Append-only: events are immutable
  - Query: `GET /v1/{tenant}/tasks/{id}/events` → audit trail
- [ ] Idempotency:
  - Use idempotency keys in request headers: `Idempotency-Key: <uuid>`
  - Store in Redis with TTL (24h)
  - Return cached response if key exists

### 3.3 SLA Timers & Escalation
- [ ] SLA calculation:
  - `sla_due_at` = `requested_dropoff_at` (from task) or `requested_pickup_at` + estimated duration (from routing, Sprint 3)
  - Background job: check tasks approaching SLA (e.g., 15min before due)
- [ ] Escalation rules (`internal/ent/schema/escalation.go`):
  - `escalation_rules` table: `tenant_id`, `rule_type` (sla_breach/long_delay), `threshold_minutes`, `action` (notify/reassign/alert), `metadata`
- [ ] Escalation service:
  - Monitor tasks: if `sla_due_at` < now + threshold, trigger escalation
  - Actions: send notification to dispatcher, reassign to different rider, alert operations team
  - Emit `logistics.sla.breach` event to notifications service
- [ ] Exception taxonomy:
  - `delivery_incidents` table: `task_id`, `incident_type` (damage/late/customer_unavailable), `description`, `reported_by`, `reported_at`, `resolution_status`, `resolved_at`
  - API: `POST /v1/{tenant}/tasks/{id}/incidents` → report exception
- [ ] SLA reporting:
  - `GET /v1/{tenant}/tasks/sla-stats` → on-time %, average delay, breach count

## Dependencies

- Sprint 1 complete (fleet members exist for assignment)
- Auth-service for user/actor identification
- Notifications service (optional) for escalation alerts

## Acceptance Criteria

- [ ] Tasks can be created with multi-step pickups/dropoffs
- [ ] State machine enforces valid transitions
- [ ] Assignments work with rider acceptance/decline
- [ ] SLA timers calculate correctly and trigger escalations
- [ ] All state changes are audited in `task_events`
- [ ] Idempotency prevents duplicate task creation

## Next Sprint Preview

Sprint 3 will add routing and dispatch capabilities (nearest-driver, geocoding, ETA calculation, route optimization).

