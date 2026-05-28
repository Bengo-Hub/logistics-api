# Sprint 6 – Connectors (Carriers & Smart Locks) (Weeks 12-13)

**Status**: Foundation Complete (seal_number tracking done; full smart lock OAuth pending)  
**Start Date**: 2026-05-28  
**Last Updated**: 2026-05-28

## ✅ Completed (2026-05-28)

| Task | File | Notes |
|------|------|-------|
| `seal_number` field on Task | `internal/ent/schema/task.go` | Physical seal/lock ID captured in audit trail |
| `seal_number` field on Shipment | `internal/ent/schema/shipment.go` | Shipment-level seal tracking for KEMSA |
| Shipment dispatch endpoint | `internal/http/handlers/shipment.go` | `POST /{tenant}/shipments/{id}/dispatch` records seal/dispatch time |
| Chain of custody sealed/unsealed events | `internal/http/handlers/shipment.go` | `event_type: "sealed" \| "unsealed"` in custody ledger |

## Goals

- Build generic connector framework for third-party carriers and smart locks.
- Implement smart lock integrations (August, Yale, Nuki, Schlage) with access windows, ephemeral keys, and PoD enforcement.
- Add rate shopping, label/manifest generation, and cost capture by job.

## Detailed Tasks

### 7.1 Generic Connector Framework
- [ ] Connector interface (`internal/modules/integrations/connector.go`):
  ```go
  type CarrierConnector interface {
    CreateJob(ctx, task) (carrierJobID, error)
    GetStatus(ctx, carrierJobID) (status, error)
    CancelJob(ctx, carrierJobID) error
    GetLabel(ctx, carrierJobID) (labelURL, error)
  }
  ```
- [ ] Connector registry:
  - `carrier_partners` table: `provider_code` (bolt/uber/dhl), `api_credentials_json` (encrypted), `status`
  - Factory pattern: `GetConnector(providerCode) → Connector`
- [ ] Error handling:
  - Retry on transient errors (5xx, network timeout)
  - Circuit breaker: disable connector after N failures
  - Store failures in `carrier_callbacks` table for audit

### 7.2 Smart Lock Integrations
- [ ] Smart lock provider abstraction:
  - Interface: `SmartLockProvider` with methods: `CreateAccessWindow()`, `RevokeAccess()`, `GetEvents()`
- [ ] August Lock integration:
  - OAuth2 flow: tenant authorizes August account
  - API: create temporary access code for delivery window
  - Webhook: receive lock events (unlock/lock) → trigger PoD update
- [ ] Yale/Nuki/Schlage integrations:
  - Similar pattern: OAuth + API calls
  - Store credentials in `provider_credentials` (encrypted)
- [ ] Access window management:
  - `access_windows` table: `id`, `tenant_id`, `task_id`, `lock_id`, `start_at`, `end_at`, `access_code`, `status`
  - API: `POST /v1/{tenant}/locks/access-windows` → create window for task
  - Auto-revoke after `end_at` or task completion
- [ ] PoD enforcement:
  - When lock unlock event received → mark task as delivered (if within access window)
  - Store in `smart_lock_events` table: `access_window_id`, `event_type`, `occurred_at`
  - Link to `proof_of_delivery` record

### 7.3 Rate Shopping & Cost Capture
- [ ] Rate shopping:
  - `POST /v1/{tenant}/tasks/{id}/rate-shop` → query multiple carriers for quotes
  - Compare: price, ETA, reliability score
  - Store offers in `marketplace_offers` table
- [ ] Label/manifest generation:
  - `GET /v1/{tenant}/carrier-jobs/{id}/label` → download shipping label (PDF)
  - `GET /v1/{tenant}/carrier-jobs/{id}/manifest` → download manifest (CSV/PDF)
  - Store URLs in `carrier_jobs.label_url`, `carrier_jobs.manifest_url`
- [ ] Cost capture:
  - When carrier job completes, store cost in `carrier_jobs.cost_amount`, `carrier_jobs.currency`
  - Export to treasury: emit `billing_events` with `event_type='carrier_cost'` (Sprint 7)

## Dependencies

- Sprint 2 complete (tasks exist for carrier job linking)
- Provider credentials configured (carrier APIs, smart lock OAuth)
- Treasury service (optional) for cost export

## Acceptance Criteria

- [ ] Generic connector framework supports multiple carriers
- [ ] Smart lock integrations create access windows successfully
- [ ] Lock events trigger PoD updates
- [ ] Rate shopping returns quotes from multiple carriers
- [ ] Labels and manifests are generated and downloadable
- [ ] Carrier costs are captured and exported to treasury

## Next Sprint Preview

Sprint 7 will implement billing, expenses, and subscription features (usage metrics, tariffs, payouts, expense export to treasury).

