package subscriptions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// entitlementCacheTTL bounds how long a tenant's entitlement snapshot is reused by event
// consumers — 60s absorbs event bursts without hammering subscriptions-api while staying fresh.
const entitlementCacheTTL = 60 * time.Second

// Entitlements is the partial subscription snapshot used by event consumers to gate
// cross-service data sync. Demo-bypass and service-charge (PAYG) tenants are exempt.
type Entitlements struct {
	Features     []string `json:"features"`
	Status       string   `json:"status"`
	BillingMode  string   `json:"billing_mode"`
	IsDemoBypass bool     `json:"is_demo_bypass"`
	// ActiveProducts is the product_code of every currently-active per-product subscription
	// line — used by ConsumerHasActiveProduct to gate cross-service traffic against a product
	// the tenant has self-deactivated (distinct from Features, which is plan ENTITLEMENT — a
	// tenant can be entitled to a product and still have turned it off). Empty/nil for exempt
	// tenants (subscriptions-api's exemptResult never populates it) — ConsumerHasActiveProduct
	// bypasses via BillingMode, never by checking this list.
	ActiveProducts []string `json:"active_products"`
}

type cachedEntitlements struct {
	ent     *Entitlements
	fetched time.Time
}

var (
	entCacheMu sync.Mutex
	entCache   = map[string]cachedEntitlements{}
)

// GetEntitlements fetches the tenant's subscription snapshot (features, status,
// billing_mode) from the S2S endpoint. Returns nil on any error so callers fail open.
func (c *Client) GetEntitlements(ctx context.Context, tenantID string) *Entitlements {
	if c == nil || c.cfg.ServiceURL == "" {
		return nil
	}
	url := fmt.Sprintf("%s/api/v1/tenants/%s/subscription", c.cfg.ServiceURL, tenantID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("X-Tenant-ID", tenantID)
	if c.cfg.APIKey != "" {
		req.Header.Set("X-API-Key", c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var e Entitlements
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return nil
	}
	return &e
}

// ConsumerHasFeature reports whether a tenant is entitled to featureCode, for NATS event
// consumers that carry a tenant_id but no user JWT. Mirrors authclient.IsGatingExempt:
// demo-bypass and service-charge (PAYG) tenants are always allowed; otherwise the feature
// must be present. FAILS OPEN (returns true) on subscriptions-api outage / missing snapshot
// so a downtime never silently drops legitimate data sync. Cached per tenant for the TTL.
func (c *Client) ConsumerHasFeature(ctx context.Context, tenantID, featureCode string) bool {
	if c == nil || tenantID == "" {
		return true // not wired → fail open
	}
	e := c.cachedEntitlements(ctx, tenantID)
	if e == nil {
		return true // lookup failed → fail open
	}
	if e.IsDemoBypass || e.BillingMode == "service_charge" {
		return true // exempt — mirror IsGatingExempt
	}
	for _, f := range e.Features {
		if f == featureCode {
			return true
		}
	}
	return false
}

// ConsumerHasActiveProduct reports whether tenantID has currently ACTIVATED productCode — used
// to gate cross-service traffic (S2S calls into this service, NATS events this service reacts
// to) against a product the tenant has self-deactivated via subscriptions-api's per-product
// activation flow. Distinct from ConsumerHasFeature: that checks plan ENTITLEMENT (can the
// tenant use this at all); this checks ACTIVATION (has the tenant currently turned it on).
//
//   - Exempt (BillingMode == "exempt") and service-charge/PAYG tenants always pass — checked via
//     BillingMode, NEVER by looking for the product in ActiveProducts (exemptResult never
//     populates that list).
//   - FAILS OPEN (returns true) when the client is nil, tenantID/productCode is empty,
//     subscriptions-api is unreachable, or ActiveProducts is completely EMPTY (a tenant
//     subscription that pre-dates per-product self-activation and was never backfilled — see
//     subscriptions-api's product_id/backfill fix). A subscriptions-api outage — or an
//     unmigrated tenant — must never silently strand a legitimate delivery.
//
// Callers should treat a false result as "skip this tenant" (ack/no-op), never as an error to
// retry — mirroring the existing entitled()/hasFeature skip-not-fail pattern already used by
// OrderReadyConsumer.
func (c *Client) ConsumerHasActiveProduct(ctx context.Context, tenantID, productCode string) bool {
	if c == nil || tenantID == "" || productCode == "" {
		return true // not wired / no product to check → fail open
	}
	e := c.cachedEntitlements(ctx, tenantID)
	if e == nil {
		return true // lookup failed → fail open
	}
	if e.BillingMode == "exempt" || e.BillingMode == "service_charge" {
		return true
	}
	if len(e.ActiveProducts) == 0 {
		return true // pre-migration tenant, never backfilled
	}
	for _, p := range e.ActiveProducts {
		if p == productCode {
			return true
		}
	}
	return false
}

func (c *Client) cachedEntitlements(ctx context.Context, tenantID string) *Entitlements {
	entCacheMu.Lock()
	if hit, ok := entCache[tenantID]; ok && time.Since(hit.fetched) < entitlementCacheTTL {
		entCacheMu.Unlock()
		return hit.ent
	}
	entCacheMu.Unlock()

	e := c.GetEntitlements(ctx, tenantID)
	if e == nil {
		return nil // do not cache failures — retry next event
	}
	entCacheMu.Lock()
	entCache[tenantID] = cachedEntitlements{ent: e, fetched: time.Now()}
	entCacheMu.Unlock()
	return e
}
