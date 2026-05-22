# Sprint 2 – Task Lifecycle

**Status**: ✅ DONE (core complete; SLA monitor added post-sprint)
**Completed**: 2026-03-30

## Goals

- Task entities with finite state machine (FSM)
- Create/assign/accept/complete flows with auditing
- ordering.order.ready NATS consumer → auto-create task
- SLA timers, breach events, escalation levels

## Completed

### Task Entities & FSM ✅
- Ent schemas: `task.go`, `task_step.go`, `task_assignment.go`, `proof_of_delivery.go`
- FSM via `validTransitions` map in `tasks/service.go`; invalid transitions return 422
- All state changes publish NATS events via outbox pattern

### Task API ✅
- `POST /tasks` — create task (external_reference + source_service idempotency)
- `GET /tasks` — list (status filter, outlet_id filter via X-Outlet-ID header)
- `GET /tasks/{id}` — detail with steps, assignment, PoD
- `PATCH /tasks/{id}/status` — status progression (FSM enforced)
- `POST /tasks/{id}/assign` — assign to fleet member
- `POST /tasks/{id}/dispatch` — manually trigger auto-dispatcher
- `GET /tasks/{id}/pod` — retrieve proof of delivery
- `POST /tasks/{id}/pod` — submit PoD (photo URL, signature, OTP, notes)
- `POST /tasks/{id}/rate` — customer rates rider
- `GET /tasks/{id}/stream` — SSE stream for real-time status updates

### NATS Consumers ✅
- `OrderReadyConsumer`: subscribes `ordering.order.ready` → creates + auto-dispatches task
- `TransferReadyConsumer`: subscribes `inventory.transfer.created` → creates outlet_transfer task

### Outbox Events ✅
- `logistics.task.assigned`, `logistics.task.accepted`, `logistics.task.en_route`,
  `logistics.task.completed`, `logistics.task.status_changed`
- `published_at` field on outbox rows; reliable delivery via NATS JetStream

### Proof of Delivery ✅
- `proof_of_delivery` schema: photo_url, signature_url, notes, otp_verified,
  cod_collected, amount_collected, tenant_id

### COD & Customer Rating ✅
- Cash-on-delivery fields in PoD; `customer_rating`, `customer_feedback` on task

### SLA Monitor ✅
- `SLAMonitor` goroutine checks non-terminal tasks past `sla_due_at` every 5 minutes
- Publishes `logistics.task.sla_breached` NATS event per overdue task
- `EscalationLevel()`: classifies breach as warning (15m) / critical (30m) / escalated (60m)
- `GetOverdueTasks()` tenant-scoped query for admin dashboard

### ETA Recalculation ✅
- `ETAUpdater` polls active tasks every 30 seconds
- Also triggered immediately on `accepted` and `en_route` status transitions

## Backlog

- Escalation rule table (`escalation_rules`) for tenant-configurable thresholds — deferred
- Incident/exception reporting (`delivery_incidents` table) — deferred to Sprint 5
- SLA stats endpoint (`GET /tasks/sla-stats`) — deferred to Sprint 3 analytics
