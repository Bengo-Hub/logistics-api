# Sprint 7 – Billing, Expenses & Subscription (Weeks 14-15)

**Status**: Not Started  
**Start Date**: TBD  
**Target Completion**: TBD

## Goals

- Implement usage metrics (tasks, km) with plan gating for advanced features.
- Build tariff system (base fare, per-km, per-minute, surcharges) with payout calculations.
- Create expense export to treasury (fuel/toll/parking/maintenance) with GL mapping and cost centers.
- Add receipts upload and mileage reimbursement support.

## Detailed Tasks

### 8.1 Usage Metrics & Plan Gating
- [ ] Usage tracking:
  - `usage_metrics` table: `tenant_id`, `metric_type` (tasks/km/api_calls), `period_start`, `period_end`, `value`, `limit` (from subscription)
  - Background job: aggregate daily/weekly/monthly usage
- [ ] Subscription integration:
  - Query treasury/auth-service for plan entitlements: `GET /v1/{tenant}/subscription`
  - Extract: `max_tasks_per_month`, `max_km_per_month`, `features[]` (advanced_routing, marketplace, analytics)
- [x] Feature gating (status 2026-10-10): `RequireFeature` reads `subscription_features` from the token for `rider_management` (fleet writes), `live_tracking` (fleet map and WebSocket) and `route_optimisation` (matrix). Plan limits `routing_requests_per_month` and `live_tracking_requests_per_month` count per calendar month (shared-ratelimit v0.3.0 `NewMonthlyQuota`); the old `*_per_day` keys never matched the catalog, so caps were off until `b8daf3f`.
  - Tiers (subscriptions-api `e1505d0`): tier 1 rider management, assignment, dispatch, delivery areas; tier 2 adds driver analytics and performance reports; tier 3 adds live tracking, route optimisation, API access. Prod re-seed pending approval.
  - Open: logistics-ui pages still need the treasury-style `SubscriptionGate` on every gated page and badges on all gated sidebar items.
- [ ] Usage API:
  - `GET /v1/{tenant}/usage/metrics` → current usage vs limits
  - `GET /v1/{tenant}/usage/overage` → overage charges (if applicable)

### 8.1b Access control (status 2026-10-10)
- [x] System roles (admin, dispatcher, driver) defined once (`rbac.SystemRoles`), ensured for every tenant by the startup seed and on demand; one permission rule (`rbac.Service.HasPermission`) with tenant admin bypass and SSO role mapping; driver role limited to own work; fleet and notification reads gated; Roles page API with usable views (`e366fbe`, `ef7c7b7`).
- [x] Tenant settings reduced to what code reads (`auto_assign_enabled`, `pod_required`, now enforced for riders); eight dead keys retired (`c311ffd`).

### 8.2 Tariffs & Payouts
- [x] Customer delivery tariffs, done 2026-10-10 without a separate table: area fees on zones plus the per-tenant `logistics.delivery_quote_policy` (per-km, minimum, rounding, geofence buffer); one quote rule for every service (`docs/delivery-zones.md`). Rider pay uses the order's delivery fee, else the rider pricing rules, else the same policy by real trip distance.
- [ ] Tariff profiles with per-minute and time-of-day surcharges (remaining scope; `tariff_profiles` table, see ERD):
  - `name`, `base_fare`, `per_km_rate`, `per_minute_rate`, `surcharge_json` (peak_time/overnight), `is_active`
- [ ] Fare calculation:
  - `CalculateFare(ctx, task, tariffProfile)` → uses `route_metrics.actual_distance_meters`, `actual_duration_seconds`
  - Apply surcharges: check `requested_pickup_at` for peak/overnight
  - Store in `tariff_applications` table
- [x] Staff (salaried) riders, done 2026-10-10: `fleet_members.metadata.employment` = `freelance` (paid per delivery) or `staff` (erp-api payroll, no per-task pay unless `per_task_earnings`). Per diem and allowances are erp-api expense claims: on completion logistics calls `POST /hrm/claims/external` with the trip facts and keeps `metadata.per_diem_claim` on the task; rates, job group rates, minimum distance, tax and approval live in erp-api's `hrm.per_diem_policy`. Endpoints: `PUT /fleet/members/{id}/employment`, `POST /tasks/{id}/per-diem`, `POST /riders/me/tasks/{id}/per-diem`. See `docs/integrations.md`.
  - Open: rider-app staff view (trips and claim status instead of earnings, claim with days); erp-ui per diem policy card.
- [ ] Payout calculation:
  - `earnings_statements` table: `fleet_member_id`, `period_start`, `period_end`, `gross_amount`, `net_amount` (after deductions), `bonus_amount`, `deduction_amount`
  - Background job: aggregate completed tasks per driver per period
  - Apply deductions: platform fee, penalties (SLA breaches, incidents)
- [ ] Payout export:
  - Emit `billing_events` to treasury: `event_type='payout'`, `amount`, `fleet_member_id`, `period`
  - Treasury processes payout via MPesa B2C or bank transfer

### 8.3 Expense Export to Treasury

Note 2026-10-10: staff rider per diem and allowances do not go through this path; they are erp-api claims (see 8.2). Fuel, toll and parking for staff drivers should follow the same erp-api claim route rather than a logistics expenses table.
- [ ] Expense ingestion:
  - `POST /v1/{tenant}/expenses` → accepts expense from logistics (fuel, toll, parking, maintenance)
  - Required fields: `route_id`, `vehicle_id`, `driver_id`, `category`, `amount`, `currency`, `cost_center`, `project`, `receipt_url`
  - Store in local `expenses` table (temporary) or emit directly to treasury
- [ ] GL mapping:
  - Category → GL account mapping (configurable per tenant)
  - Defaults: `fuel` → `Expenses:Vehicle:Fuel`, `toll` → `Expenses:Vehicle:Toll`
  - Treasury receives expense with `gl_account_code` for posting
- [ ] Cost center support:
  - `cost_center` field links to treasury cost center (for multi-branch reporting)
  - `project` field for project-based allocation
- [ ] Receipt upload:
  - `POST /v1/{tenant}/expenses/{id}/receipt` → upload receipt image (S3)
  - Store URL in `expenses.receipt_url`
  - Treasury can fetch receipt for invoice matching
- [ ] Mileage reimbursement:
  - `POST /v1/{tenant}/mileage-logs` → driver logs mileage (start/end odometer, route_id)
  - Calculate: `(end_odometer - start_odometer) * per_km_rate` (from policy)
  - Create expense automatically: `category='mileage_reimbursement'`
  - Export to treasury as expense

## Dependencies

- Sprint 2 complete (tasks exist for fare calculation)
- Sprint 3 complete (routes exist for distance/duration)
- Treasury service exposes expense/bill/journal APIs
- Auth-service exposes subscription entitlements

## Acceptance Criteria

- [ ] Usage metrics are tracked accurately
- [ ] Feature gating blocks unauthorized features
- [ ] Tariffs calculate fares correctly with surcharges
- [ ] Payouts are calculated and exported to treasury
- [ ] Expenses are ingested and exported with GL mapping
- [ ] Receipts are uploaded and linked to expenses
- [ ] Mileage logs create reimbursement expenses automatically

## Next Sprint Preview

Sprint 8 will add reverse logistics (returns/exchanges), PoD upgrades (photos/NFC), compliance (GDPR, data retention), and safety features (no-go zones, HAZMAT).

