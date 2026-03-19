# Sprint 5 – External Signals & Alt-Routes (Weeks 10-11)

**Status**: Not Started  
**Start Date**: TBD  
**Target Completion**: TBD

## Goals

- Integrate POS/inventory/cafe webhooks for order readiness and transfer triggers.
- Add traffic incident ingestion (accidents, closures, hazards) with alternative route prompts.
- Implement weather forecast integration for ETA/routing penalties and advisories.
- Harden bulk import/export and webhook signing/retries.

## Detailed Tasks

### 6.1 POS/Inventory/Cafe Hooks
- [ ] Webhook handlers:
  - `POST /v1/webhooks/pos/order-ready` → receives `pos.order.ready` event
  - `POST /v1/webhooks/inventory/transfer-created` → receives `inventory.transfer.created`
  - `POST /v1/webhooks/cafe/order-ready` → receives `cafe.order.ready` event
- [ ] Task creation from webhooks:
  - Validate webhook signature (HMAC-SHA256)
  - Extract `tenant_id`, `external_reference`, `source_service`
  - Create task via existing `CreateTask` service (Sprint 2)
  - Emit `logistics.task.created` event back to source service
- [ ] Inventory availability query:
  - `GET /v1/{tenant}/inventory/availability?zone={z}&branch={b}&sku={s}` → query inventory service
  - Use response to inform dispatch: prefer nearest branch with stock (see ERD alignment)
- [ ] Webhook retry logic:
  - Store failed webhooks in `outbox_events` with retry count
  - Exponential backoff: retry after 1min, 5min, 15min, 1hr
  - Dead letter queue (DLQ) after max retries

### 6.2 Traffic Incidents
- [ ] Incident ingestion:
  - Google Maps Traffic Layer API (if licensed) or third-party incident feeds
  - Store in `route_incidents_observed` table: `id`, `tenant_id`, `incident_type` (accident/closure/hazard), `location` (geo_point), `severity`, `reported_at`, `resolved_at`, `metadata`
- [ ] Alternative route calculation:
  - When incident detected on planned route, query routing provider for alternatives
  - Compare: original route vs alternative (distance, duration, cost)
  - Store in `alternative_route_suggestions` table: `route_id`, `original_route_json`, `alternative_route_json`, `penalty_seconds`, `suggested_at`
- [ ] Alt-route prompts:
  - `POST /v1/{tenant}/routes/{id}/alternatives` → get alternative routes
  - `POST /v1/{tenant}/routes/{id}/switch` → dispatcher/rider accepts alternative
  - Update task route and notify customer of ETA change

### 6.3 Weather Integration
- [ ] Weather provider abstraction:
  - OpenWeather One Call API or Tomorrow.io
  - Provider config stored in `provider_credentials` (Sprint 0)
- [ ] Forecast ingestion:
  - Background job: fetch forecasts for active task destinations
  - Store in `weather_forecasts` table: `id`, `tenant_id`, `location` (geo_point), `forecast_json`, `valid_from`, `valid_to`, `provider`
- [ ] ETA/routing penalties:
  - Adjust ETA based on weather: rain/snow adds time penalty
  - Routing: avoid routes with severe weather (if alternative exists)
  - Store penalties in `route_metrics.weather_penalty_seconds`
- [ ] Weather advisories:
  - Emit `logistics.weather.advisory` event to notifications service
  - Alert dispatchers/riders of severe weather on route

### 6.4 Bulk Import/Export & Webhook Hardening
- [ ] Bulk import:
  - `POST /v1/{tenant}/import/tasks` → CSV/JSON upload
  - Async job: `import_jobs` table tracks status
  - Validation: check tenant, geocode addresses, validate fleet members
- [ ] Bulk export:
  - `POST /v1/{tenant}/export/tasks` → generate CSV/JSON
  - Parameters: date range, filters (status, fleet, task type)
  - Async job: `export_jobs` table, result URL in S3 or returned via webhook
- [ ] Webhook signing:
  - Generate HMAC-SHA256 signature using shared secret
  - Header: `X-Webhook-Signature: sha256=<hash>`
  - Validate on receive: reject if signature mismatch
- [ ] Webhook retries (enhanced):
  - Idempotency: use `X-Idempotency-Key` header
  - Retry with exponential backoff (see 6.1)
  - DLQ monitoring: alert on high failure rate

## Dependencies

- Sprint 2 complete (tasks exist for webhook-triggered creation)
- Sprint 3 complete (routing exists for alt-route calculation)
- Provider credentials configured (Google Maps, weather APIs)
- Inventory service exposes availability API

## Acceptance Criteria

- [ ] Webhooks from POS/inventory/ordering-backend create tasks successfully
- [ ] Traffic incidents are detected and stored
- [ ] Alternative routes are calculated and suggested
- [ ] Weather forecasts adjust ETAs and routing
- [ ] Bulk import/export jobs complete successfully
- [ ] Webhook signatures are validated and retries work correctly

## Next Sprint Preview

Sprint 6 will add carrier and smart lock connectors (generic framework, August/Yale/Nuki integrations, rate shopping).

