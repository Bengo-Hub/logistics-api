# Logistics Driver/Rider Onboarding Flows

## Overview

The logistics service supports 3 distinct onboarding workflows based on the `fleet_type` field on the Fleet entity. Each use case has different driver management, KYC, and fleet control requirements.

## Fleet Types

| Fleet Type | Use Case | Example | Driver Control |
|-----------|----------|---------|---------------|
| `courier` | Company-managed fleet | DHL, G4S, Wells Fargo | Company onboards drivers, manages compliance, pays salary |
| `distribution` | Warehouse-to-warehouse | KEMSA, supply chain | Company assigns drivers to transfer tasks |
| `delivery` | Public delivery platform | Uber, Bolt, Glovo | Self-service signup OR manual invite, KYC review by tenant |

## Status Lifecycle

```
invited → pending → pending_review → active
                                  ↘ rejected
                     active → suspended → active (reactivate)
```

| Status | Description | Triggered By |
|--------|-------------|-------------|
| `invited` | Email invitation sent, awaiting SSO signup | Admin sends invite |
| `pending` | SSO complete, awaiting KYC document upload | Rider registers via SSO |
| `pending_review` | KYC uploaded, awaiting admin review | Rider saves profile with docs |
| `active` | Approved, can accept deliveries | Admin approves |
| `rejected` | Application denied with reason | Admin rejects |
| `suspended` | Temporarily deactivated | Admin suspends |

## Use Case 1: Courier Company (fleet_type = courier)

**Scenario**: DHL/G4S/Wells Fargo style — company has full control of its fleet.

### Flow
1. Company admin creates vehicles via `POST /fleet/vehicles`
2. Admin batch-invites drivers via `POST /fleet/members/batch`
3. Drivers receive invite email → SSO signup → KYC form
4. **For courier fleets**: Company may auto-approve drivers (skip KYC review) since compliance is managed externally
5. Admin assigns vehicles to drivers via `POST /fleet/members/{id}/vehicle`
6. Drivers start receiving task assignments

### Key Endpoints
- `POST /fleet/vehicles` — Create vehicle with type/make/model/plate
- `POST /fleet/members/batch` — Batch invite by email array
- `POST /fleet/members/{id}/vehicle` — Assign vehicle to driver
- `POST /fleet/members/{id}/approve` — Approve driver

### Compliance
- Vehicle insurance tracking (`insurance_expiry`, `insurance_document`)
- Inspection certificates (`inspection_expiry`, `inspection_document`)
- Driver licensing (`license_no`, `id_number`)
- All documents stored as URLs from media upload

---

## Use Case 2: Central Warehouse Distribution (fleet_type = distribution)

**Scenario**: KEMSA style — HQ warehouse → regional warehouses → hospital delivery.

> **Updated 2026-05-28**: Shipment + ChainOfCustody entities now fully implemented. See `docs/erd.md#distribution--kemsa`.

### Flow

#### A. Warehouse-to-Warehouse Transfer
1. Inventory service creates a StockTransfer (source → destination warehouse)
2. `inventory.transfer.created` event published to NATS
3. Logistics service auto-creates a Task with `task_type=transfer`
4. Dispatcher creates a **Shipment** (`POST /{tenant}/shipments`) grouping the transfer tasks
5. Dispatcher sets cold-chain parameters (`temperature_min_celsius`, `temperature_max_celsius`) and `seal_number`
6. `POST /{tenant}/shipments/{id}/dispatch` dispatches the shipment — records dispatch timestamp + seal
7. Chain of custody event `released` is recorded at source facility
8. Driver picks up batch, travels to destination warehouse
9. At destination, driver and receiving staff record `received` custody event with:
   - `received_quantity` — units verified at handover
   - `receiving_staff_name` — name of receiving staff
   - `signature_url` — receiving staff signature
   - `temperature_reading` — cold chain verification
10. If temperature breaches range → custody event `temperature_breach` recorded → notification published
11. `logistics.task.completed` event published → inventory marks transfer as received

#### B. Hospital Delivery (Multi-Step PoD)
1. Dispatcher creates a `hospital_delivery` shipment
2. Tasks assigned to distribution fleet driver
3. At hospital, extended PoD is captured:
   - `POST /{tenant}/tasks/{id}/pod` with `receiving_staff_name`, `condition_on_arrival`, `received_quantity`, `batch_reference`
4. Chain of custody `received` event recorded with full audit fields
5. Report generated via `GET /{tenant}/reports/shipment-manifest/{shipmentId}`

### Key Endpoints (Added 2026-05-28)
| Endpoint | Description |
|----------|-------------|
| `POST /{tenant}/shipments` | Create distribution batch |
| `GET /{tenant}/shipments` | List shipments with filters |
| `GET /{tenant}/shipments/{id}` | Detail + custody ledger |
| `POST /{tenant}/shipments/{id}/dispatch` | Dispatch with seal recording |
| `POST /{tenant}/shipments/{id}/custody` | Add custody event |
| `GET /{tenant}/shipments/{id}/custody` | Full custody ledger |

### Integration Points
- **inventory-api**: StockTransfer, Warehouse (with lat/lng), StockTransferLine
- **logistics-api**: Task (task_type=transfer), Shipment, ChainOfCustody
- **Routing**: Valhalla engine for optimal routes between warehouses
- **Tracking**: Real-time GPS via WebSocket; temperature via IoT sensor → `telemetry_points.temperature_celsius`

### Proximity-Based Supply
- Warehouses have GPS coordinates (`latitude`, `longitude`)
- When a regional warehouse has a shortage, the system identifies the closest warehouse with stock
- Transfer task routes to the nearest fulfillment point

---

## Use Case 3: Public Delivery Platform (fleet_type = delivery)

**Scenario**: Uber/Bolt style — two paths for rider onboarding.

### Path A: Manual Invite
1. Tenant admin clicks "Invite Rider" on dashboard
2. Enters rider email + optional ID number
3. Rider receives email with "Accept Invitation & Sign Up" link
4. Link points to: `{riderAppUrl}/join?org={tenantSlug}&invite_code={code}`
5. Rider clicks → SSO registration (with tenant context)
6. After SSO → redirected to rider-app `/profile` page
7. Rider uploads KYC documents (ID/passport, photo, vehicle images)
8. Status changes: `invited` → `pending` → `pending_review`
9. Tenant admin receives "New KYC Submission" email notification
10. Admin reviews KYC in dashboard → approve or reject
11. Rider receives approval/rejection email

### Path B: Public Signup
1. User clicks "Sign up to deliver" on ordering-frontend menu
2. Link points to: `{riderAppUrl}/join?org={tenantSlug}` (no invite_code)
3. Join page shows "Sign Up to Deliver" heading (vs "You've been invited")
4. Same SSO → KYC → review flow as Path A

### Path C: Internal Rider Added by the Admin (employment = staff)
Internal riders are the business's own employees, so there is no invite step and no KYC review.
1. Tenant admin opens Riders, Add rider, and picks "Internal rider"
2. Fills in first name, email, phone and ID number (all required), plus optional licence number
3. Chooses the pay basis (see below) and the vehicle: none yet, link a free vehicle from the fleet,
   or register a new one (a motorbike is type `motorcycle`; make and model are optional)
4. "Email them their sign-in details" is ticked by default
5. `POST /fleet/members/onboard` (`fleet.Service.OnboardInternalRider`) then runs in this order:
   - Checks first, so a refusal never leaves an account behind: already a rider here (`409`),
     linked vehicle taken or foreign, new plate already in the fleet.
   - auth-api `POST /api/v1/s2s/tenants/{tenant}/members` (role `driver`, `merge_roles`,
     `skip_welcome`) finds or creates the SSO account. A new account gets a temporary password and
     `must_change_password`, so the rider must set their own on first sign-in. Called once with no
     retries (`platform/authapi`): a retried create answers "existing user" without the password.
     auth-api down answers `502` and nothing is saved.
   - One transaction writes the local user under the auth user id (the key RBAC uses), the vehicle
     and the `active` member (`onboarding_source = company_created`). An older stub with the same
     email is moved onto the auth id (`fleet.RekeyUser`). If `auth.user.created` inserts the user
     first, the transaction runs once more and finds it. An account that already rides for another
     business answers `409` (user ids are platform-wide here).
   - Driver role granted on the auth id.
   - With the email ticked, `logistics/rider_account` goes straight to notifications-api
     (`platform/notify`, `Idempotency-Key rider-account:<member>`), never through the event outbox:
     email, temporary password (new accounts only) and a link to the rider app. An existing account
     is told to use its own password.
6. The response carries `temp_password` once (`Cache-Control: no-store`), plus `existing_account`
   and `email_sent`. The admin sees it in a one-time panel to hand over if the email did not go.
   Logistics never stores it. In transit it sits in the notifications NATS stream until retention
   drops it, as with the Digitika portal-account emails.

`fleet.RekeyUser` is the one re-key path, also used by `auth.user.created` and first sign-in
(`identity.EnsureUserFromToken`). Before 2026-10-18 both of those failed or kept the stub's random
key: the event handler created the auth-id row while the stub still held `(tenant_id, email)`, and
sign-in linked the stub in place, so an invited rider's role grants never matched their token.

ID and photo uploads are optional for internal riders. The ID number is the one identity detail
required: the API refuses an onboarding without it, and the rider-app asks for it when a staff
rider saves their profile with none on file.

Pay basis (`fleet_members.metadata.employment.pay_basis`, no new column):

| Pay basis | Who | Per-delivery earnings | Per diem |
|-----------|-----|-----------------------|----------|
| `payroll` (default for staff) | Salaried employee | None. Salary is erp-api payroll | erp-api claim raised from the trip |
| `earning_rules` | Internal rider paid like a freelancer | Yes, from the rider pricing rules | erp-api claim as for any staff rider |
| `earning_rules` (always) | Freelance rider | Yes | None |

`SetEmployment` (`PUT /fleet/members/{id}/employment`, body `{type, pay_basis}`) changes either.
The old `per_task_earnings` flag is replaced by `pay_basis`; it is still read for members saved
with it and never written. `GET /riders/me` returns `employment` and `pay_basis`, and the rider-app
shows the trips and per diem screen only to payroll riders.

### Vehicles
- One rule set in `fleet/vehicle_rules.go`: types are normalised (`bike`, `motorbike`, `boda` all
  store `motorcycle`), plates are upper-cased and space-collapsed, and a plate is unique per tenant
  (the old global unique index `vehicles_license_plate_key` was dropped on 2026-10-17).
- A vehicle belongs to one rider at a time. Linking checks both rows belong to the tenant and the
  vehicle is free (`409` otherwise). `POST /fleet/members/{id}/vehicle` with an empty `vehicle_id`
  unlinks.
- Deleting a rider keeps company vehicles. Only a vehicle the rider registered on their own profile
  (`metadata.owner = rider`) goes with them.
- `GET /fleet/vehicles?search=&status=&assigned=true|false&page=&limit=` lists every vehicle,
  assigned or not, with `assigned_to`, plus a fleet-wide `summary` counted in SQL. The Vehicles page
  and the rider vehicle pickers use it.

### 7-Day Auto-Cleanup
- Hourly background job checks for stale riders
- Riders in `invited`/`pending`/`pending_review` status older than 7 days
- Auto-deletes fleet member + vehicle records
- Publishes `fleet.member_expired` event
- Rider receives "Application Expired" email with re-apply link

### KYC Document Requirements
- ID/Passport copy (image, max 5MB)
- Rider passport photo (image, max 5MB)
- Vehicle license plate photo (image, max 5MB)
- Vehicle side view photo (image, max 5MB)
- `.docx` files NOT accepted
- PDF and image formats only

---

## Events Published

| Event | Subject | Triggered By | Notification |
|-------|---------|-------------|-------------|
| `fleet.member_invited` | `logistics.fleet.member_invited` | InviteMember | Email to rider (join link) |
| `fleet.member_kyc_submitted` | `logistics.fleet.member_kyc_submitted` | UpdateRiderProfile | Email to tenant admin (review link) |
| `fleet.member_approved` | `logistics.fleet.member_approved` | ApproveMember | Email to rider (dashboard link) |
| `fleet.member_rejected` | `logistics.fleet.member_rejected` | RejectMember | Email to rider (support link) |
| `fleet.member_suspended` | `logistics.fleet.member_suspended` | SuspendMember | Email to rider (support link) |
| `fleet.member_expired` | `logistics.fleet.member_expired` | 7-day cleanup | Email to rider (re-apply link) |

## API Endpoints

### Fleet Management
- `GET /fleet` — Get or create tenant fleet
- `GET /fleet/members` — List members (filter by status)
- `POST /fleet/members` — Invite single member
- `POST /fleet/members/batch` — Batch invite
- `POST /fleet/members/onboard` — Add an internal rider directly (active at once, optional invite email)
- `PUT /fleet/members/{id}/employment` — Set type and pay basis
- `GET /fleet/members/{id}` — Get member with user + vehicle edges
- `POST /fleet/members/{id}/approve` — Approve (pending_review → active)
- `POST /fleet/members/{id}/reject` — Reject with reason
- `POST /fleet/members/{id}/suspend` — Suspend active member
- `POST /fleet/members/{id}/vehicle` — Assign vehicle
- `DELETE /fleet/members/{id}` — Permanently delete

### Vehicle Management
- `GET /fleet/vehicles` — List vehicles (search, status, assigned filter) with fleet summary
- `POST /fleet/vehicles` — Create vehicle
- `PATCH /fleet/vehicles/{id}` and `DELETE /fleet/vehicles/{id}` — Update or delete (delete refused while linked)

### Rider Profile (Identity)
- `GET /riders/me/profile` — Get own profile
- `PATCH /riders/me/profile` — Update profile + KYC docs (triggers pending_review; staff riders keep their status and need only an ID number on file)
