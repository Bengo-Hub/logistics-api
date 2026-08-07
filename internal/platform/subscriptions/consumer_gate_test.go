package subscriptions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServer stubs subscriptions-api's GET /api/v1/tenants/{id}/subscription with a fixed
// Entitlements payload (or a non-200 status when ent is nil, to exercise the fail-open path).
func newTestServer(t *testing.T, ent *Entitlements, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ent == nil {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(ent)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(t *testing.T, ent *Entitlements, status int) *Client {
	srv := newTestServer(t, ent, status)
	return NewClient(Config{ServiceURL: srv.URL, APIKey: "test-key"})
}

// Each test uses a distinct fake tenant ID — cachedEntitlements' cache is a package-level global
// map, so reusing a tenant ID across test cases would silently read another test's cached result.
func TestConsumerHasActiveProduct_ActiveMatch(t *testing.T) {
	c := newTestClient(t, &Entitlements{ActiveProducts: []string{"pos", "logistics"}, BillingMode: "recurring"}, http.StatusOK)
	if !c.ConsumerHasActiveProduct(context.Background(), "tenant-active-match", "logistics") {
		t.Error("expected true: logistics is in ActiveProducts")
	}
}

func TestConsumerHasActiveProduct_Deactivated(t *testing.T) {
	c := newTestClient(t, &Entitlements{ActiveProducts: []string{"pos", "inventory"}, BillingMode: "recurring"}, http.StatusOK)
	if c.ConsumerHasActiveProduct(context.Background(), "tenant-deactivated", "logistics") {
		t.Error("expected false: logistics is NOT in ActiveProducts and billing mode is normal")
	}
}

func TestConsumerHasActiveProduct_ExemptBypassesEvenWithoutProduct(t *testing.T) {
	c := newTestClient(t, &Entitlements{ActiveProducts: []string{}, BillingMode: "exempt"}, http.StatusOK)
	if !c.ConsumerHasActiveProduct(context.Background(), "tenant-exempt", "logistics") {
		t.Error("expected true: exempt tenants bypass regardless of ActiveProducts")
	}
}

func TestConsumerHasActiveProduct_ServiceChargeBypasses(t *testing.T) {
	c := newTestClient(t, &Entitlements{ActiveProducts: []string{}, BillingMode: "service_charge"}, http.StatusOK)
	if !c.ConsumerHasActiveProduct(context.Background(), "tenant-payg", "logistics") {
		t.Error("expected true: service_charge (PAYG) tenants bypass")
	}
}

func TestConsumerHasActiveProduct_EmptyActiveProductsFailsOpen(t *testing.T) {
	// The critical pre-migration-tenant safety net: a normal-billing tenant with a
	// completely empty ActiveProducts list (never backfilled) must NOT be blocked.
	c := newTestClient(t, &Entitlements{ActiveProducts: nil, BillingMode: "recurring"}, http.StatusOK)
	if !c.ConsumerHasActiveProduct(context.Background(), "tenant-premigration", "logistics") {
		t.Error("expected true: empty ActiveProducts must fail open, not be read as 'deactivated'")
	}
}

func TestConsumerHasActiveProduct_UnreachableFailsOpen(t *testing.T) {
	c := newTestClient(t, nil, http.StatusInternalServerError)
	if !c.ConsumerHasActiveProduct(context.Background(), "tenant-unreachable", "logistics") {
		t.Error("expected true: a subscriptions-api outage must never strand a legitimate delivery")
	}
}

func TestConsumerHasActiveProduct_EmptyArgsFailOpen(t *testing.T) {
	c := newTestClient(t, &Entitlements{ActiveProducts: []string{"pos"}}, http.StatusOK)
	if !c.ConsumerHasActiveProduct(context.Background(), "", "logistics") {
		t.Error("expected true: empty tenantID fails open")
	}
	if !c.ConsumerHasActiveProduct(context.Background(), "tenant-empty-product", "") {
		t.Error("expected true: empty productCode fails open")
	}
}

func TestConsumerHasActiveProduct_NilClientFailsOpen(t *testing.T) {
	var c *Client
	if !c.ConsumerHasActiveProduct(context.Background(), "tenant-nil-client", "logistics") {
		t.Error("expected true: nil client (not wired) fails open")
	}
}

// ConsumerHasFeature's own contract is unchanged by this addition.
func TestConsumerHasFeature_StillWorksAlongsideActiveProducts(t *testing.T) {
	c := newTestClient(t, &Entitlements{Features: []string{"basic_logistics_access"}, ActiveProducts: []string{"logistics"}, BillingMode: "recurring"}, http.StatusOK)
	if !c.ConsumerHasFeature(context.Background(), "tenant-feature-check", "basic_logistics_access") {
		t.Error("expected true: feature present")
	}
	if c.ConsumerHasFeature(context.Background(), "tenant-feature-check-2", "nonexistent_feature") {
		t.Error("expected false: feature absent, non-exempt")
	}
}
