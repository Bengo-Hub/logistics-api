# Delivery zones, geofencing and delivery quotes

logistics-api is the single owner of service areas, geofencing and customer delivery pricing for every tenant and every service. Ordering, POS till deliveries, courier tenants and internal distribution tenants all ask logistics for a quote. None of them keep their own zones, distance maths or per-km rates.

## Concepts

**Zone.** A `geo_fences` row. The `boundary` column is always a closed polygon in GeoJSON order (`[[lng, lat], ...]`). A circle is stored as a 48-point polygon generated from its centre and radius, so every consumer can treat zones the same way. The delivery contract lives in `metadata`:

| Key | Meaning |
|---|---|
| `shape` | `circle` or `polygon` |
| `center` | `{lat, lng}`. Required for circles, derived from the bounding box for polygons |
| `radius_m` | Circle radius in metres (50 m to 500 km) |
| `fee` | Delivery fee for drop-offs inside the zone |
| `free` | `true` makes the zone free (fee forced to 0) |
| `currency` | Optional override of the policy currency |
| `min_order` | Minimum basket; quotes flag `below_min_order` when the total is lower |
| `eta_minutes` | Optional fixed ETA; otherwise estimated from distance |
| `priority` | Higher wins when zones overlap; ties go to the smaller zone |
| `outlet_ids` | Outlets the zone applies to. Empty means every outlet |
| `aliases` | Other names customers search for (e.g. "Alupe Market") |
| `notes` | Free text for admins |

Other metadata keys are preserved untouched.

`zone_type` is `delivery` (priced and accepted), `exclusion` (a no-go area, rejected even inside a delivery zone), `pickup` or `surge` (stored, not used by quotes yet). `status` is `active`, `inactive` or `draft`. Only active zones are quoted.

**Policy.** One `service_configs` row per tenant under the key `logistics.delivery_quote_policy` (JSON). A row with a null tenant is the platform default; built-in defaults apply when neither exists.

| Field | Default | Meaning |
|---|---|---|
| `fallback` | `per_km` | `per_km` charges by distance outside the areas, `none` accepts zones only |
| `coverage` | `furthest_area` | Which pins outside every area are still served at the per-km price. `furthest_area` serves anything no further from the outlet than the furthest area's far edge; `buffer` only serves pins within `buffer_km` of an area edge |
| `buffer_km` | 2 | `buffer` coverage: a pin outside every area is accepted when it is this close to one. `furthest_area` coverage: slack added to the furthest area's reach |
| `confirm_by_email` | true | A distance-priced delivery is flagged `needs_confirmation`, and the order confirmation email states its area, distance and fee for the customer to check |
| `max_radius_km` | none | Optional hard cap on straight-line distance from the outlet |
| `require_zones` | true | When false, a tenant with no zones is quoted per km within `max_radius_km` |
| `base_fee`, `per_km_rate`, `min_fee` | 0, 50, 100 | Per-km price: `max(min_fee, base_fee + per_km_rate x km)` |
| `rounding` | 10 | Per-km fees round up to a multiple of this |
| `distance_source` | `road` | `road` uses Valhalla; `straight` uses the haversine distance |
| `road_factor_fallback` | 1.25 | Straight-line multiplier when Valhalla is unavailable |
| `speed_kmh`, `prep_minutes` | 25, 15 | ETA estimate for quotes without a zone ETA |
| `quote_cache_seconds` | 300 | How long callers may cache a quote |

Rider pay without a rider pricing rule uses the same per-km price, so a tenant configures distance pricing in one place.

## Quote rule

`internal/modules/zones/quote.go` (`computeQuote`) is the only delivery pricing rule on the platform:

1. Reject invalid coordinates.
2. Pick the outlet: the requested one, else the nearest active outlet with a map pin.
3. Reject drop-offs inside an active exclusion zone (`excluded_area`), or beyond `max_radius_km` (`beyond_max_radius`).
4. If any active delivery zone serving that outlet contains the point, take the highest priority, then the smallest zone. The fee is the zone fee, or 0 for a free zone.
5. Otherwise, with `fallback = per_km`, the point is still served when it is within `buffer_km` of a zone edge, or, under `coverage = furthest_area`, when its straight-line distance from the outlet is no more than the furthest area's reach plus `buffer_km`. The fee is the per-km price on the road distance from the outlet (`method = per_km`), and `needs_confirmation` is set when `confirm_by_email` is on.
6. Otherwise the point is not serviceable (`outside_delivery_area`, or `no_delivery_coverage` when the tenant has no zones). The nearest area and its distance are returned for messaging, together with `furthest_area`, `furthest_area_km` (how far deliveries go) and `outlet_km` (how far this pin is).

**Furthest reach.** Reach is the largest straight-line distance from the outlet to any boundary point of an active delivery area that serves that outlet. A circle is stored as a 48-point polygon and distance to a point is convex, so the maximum is always at a vertex. This is why a village between the outlet and a named area (Sega, between Busia and Ugunja) is delivered by distance even though no area covers it, while a place beyond the furthest area stays out of the delivery area. Reach is per outlet: an outlet with no areas has none.

Zones, outlets and the policy are loaded once per tenant into a cached snapshot (Redis, 5 minutes) with precomputed bounding boxes and areas, so a quote is a bounding-box filter plus a ray cast. Every zone, policy and outlet change drops the snapshot.

## API

| Method and path | Auth | Purpose |
|---|---|---|
| `GET /api/v1/{tenant}/zones` | `logistics.zones.view` | All zones with settings, drafts and notes included; `outlet_id` (else `X-Outlet-ID`) keeps only zones serving that outlet |
| `POST /api/v1/{tenant}/zones` | `logistics.zones.manage` | Create (circle or polygon) |
| `PATCH /api/v1/{tenant}/zones/{id}` | `logistics.zones.manage` | Update; settings replace as a whole |
| `DELETE /api/v1/{tenant}/zones/{id}` | `logistics.zones.manage` | Delete |
| `GET /api/v1/{tenant}/zones/coverage` | public, 120/min per IP | Active zones, outlets, bounds and centre for customer maps |
| `GET /api/v1/{tenant}/zones/quote?lat&lng&outlet_id&order_total` | public, 120/min per IP | Quote for a pin (UI preview) |
| `POST /api/v1/{tenant}/zones/outlets/refresh` | `logistics.zones.manage` | Pull the tenant's outlets and map pins from auth-api now (after a pin is saved there) and drop cached quotes |
| `GET /api/v1/{tenant}/delivery-policy/suggest-fee?lat&lng&radius_m&outlet_id` | `logistics.zones.manage` | The fee the tenant's per-km rules give for a new area, from the outlet (else `X-Outlet-ID`, else the nearest outlet with a pin), at the centre and at the near and far edge |
| `GET /api/v1/{tenant}/delivery-policy` | `logistics.zones.view` | Effective policy and its source |
| `PUT /api/v1/{tenant}/delivery-policy` | `logistics.pricing.manage` | Save the tenant policy |
| `GET /api/v1/{tenant}/routing/geocode/search?q` | public, 60/min per IP | Tenant areas first, then geocoder results biased to the coverage |
| `GET /api/v1/{tenant}/routing/geocode/reverse?lat&lng` | public, 60/min per IP | Place name plus the area it falls in or is nearest to |
| `GET /api/v1/{tenant}/analytics/zones?period` | JWT | Deliveries, fees, distance and timing per zone |
| `POST /api/v1/s2s/zones/{tenant}/quote` | `X-API-Key` | Authoritative quote for checkout. Tenant is a UUID or slug |
| `GET /api/v1/s2s/zones/{tenant}/coverage` | `X-API-Key` | Coverage for server-side listings |

Rider pricing rules (`/earnings/pricing-rules`) now require `logistics.pricing.manage` to change.

**Public reads.** Only `zones/coverage`, `zones/quote`, `routing/*` and `track/*` skip authentication (`router/public_reads.go`, tested). The zone list, one zone and `analytics/zones` used to be public too, because the old rule matched any path containing `/zones`; they carry drafts, admin notes and delivery performance and now need a sign-in.

**Outlet scope.** Zones, coverage, quotes and fee suggestions follow the outlet in `outlet_id` or, for signed-in callers, `X-Outlet-ID` (the header switcher). Zone names are unique per tenant, so a second outlet serving the same place at its own fee needs its own name (for example "Alupe (Kakamega branch)"). The analytics endpoints (`kpis`, `series`, `riders`, `zones`) take the same header and add `AND outlet_id = $n` only when a drill-down is set, which keeps the `(tenant_id, outlet_id, created_at)` index usable.

**Outlet pins.** auth-api owns outlets and their map pins. logistics-ui's Delivery areas, Outlets tab saves a pin to auth-api (`PUT /tenants/{slug}/outlets/{id}`, tenant admins only) and then calls `zones/outlets/refresh`, so a new pin prices quotes immediately instead of after the outlet event or the 6 hourly resync.

The geocoder proxy calls a Nominatim-compatible service (`GEOCODER_URL`, default the public OpenStreetMap instance) with a proper `GEOCODER_USER_AGENT`. Results are cached in Redis (search 1 hour, reverse 24 hours at about 11 m precision) and outbound calls are limited to one per second across all pods. Browsers never call a geocoder directly.

## How consumers use it

- **ordering-backend** calls the S2S quote at checkout with the customer's pin and the outlet, charges the returned fee, stores the quote on the order and sends `delivery_zone_id`, `delivery_zone_name` and `distance_km` in `ordering.order.ready`.
- **Customer UIs** call `zones/coverage` to draw deliverable areas, `routing/geocode/*` to search and name places, and `zones/quote` to preview the fee while a pin moves.
- **Task intake** tags every delivery task with `zone_id`, `zone_name` and `distance_km` in its metadata (trusting an upstream quote when one is sent). Auto-dispatch puts riders whose active shift lists the task's zone ahead of other nearby riders. Rider earnings use the recorded distance.
- **Outlets** come from `auth.outlet.*` events with their `latitude` and `longitude`. Outlets of any use case are mirrored for tenants that exist in logistics, because a cafe or pharmacy dispatches deliveries too.

## Seeding defaults

`logistics-seed-delivery-zones -tenant <slug> [-dry-run] [-overwrite]` loads a tenant preset from `cmd/seed-delivery-zones`. It only creates missing zones and the policy; existing zones keep the values admins saved unless `-overwrite` is passed.

The `urban-loft` preset is a free 3 km "Busia Town" circle around the Busia outlet, 16 named areas with the published fees, and a policy of KES 50 per road km, minimum KES 100, rounded up to KES 10, with a 2 km buffer. Bugengi, Ochude and Redcross could not be placed from OpenStreetMap and are seeded as drafts until an admin sets their centres in logistics-ui.
