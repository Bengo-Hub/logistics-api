package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Bengo-Hub/httpware"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const crmLinksKey = "test-service-key"

// crmLinksRouter mounts the handler the way the router does: the service key checked on the route.
func crmLinksRouter(h *CRMLinksHandler) http.Handler {
	r := chi.NewRouter()
	r.With(httpware.RequireServiceKey(crmLinksKey)).Get("/api/v1/s2s/{tenant}/crm-links/{contactID}", h.Referenced)
	return r
}

func askCRMLinks(r http.Handler, tenant, contact string, withKey bool) (int, map[string]any) {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/s2s/"+tenant+"/crm-links/"+contact, nil)
	if withKey {
		req.Header.Set("X-API-Key", crmLinksKey)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

// marketflow asks, before erasing a CRM contact, whether logistics still links it. A task carrying
// it (in any tenant) answers yes whichever known tenant asks; no task answers no. The tenant may be
// the UUID or the slug and must be known (else 404); the route needs the service key; a failed
// read is a 500, never "not referenced".
func TestCRMLinksAnswers(t *testing.T) {
	tenantA, tenantB, linked, free := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	known := map[string]uuid.UUID{tenantA.String(): tenantA, "tenant-a": tenantA, tenantB.String(): tenantB}
	h := &CRMLinksHandler{
		tenantID: func(_ context.Context, raw string) (uuid.UUID, error) {
			if id, ok := known[raw]; ok {
				return id, nil
			}
			return uuid.Nil, errTenantNotFound
		},
		referenced: func(_ context.Context, contactID uuid.UUID) (bool, error) {
			return contactID == linked, nil // a delivery task under tenant A
		},
		log: zap.NewNop(),
	}
	r := crmLinksRouter(h)

	for _, tc := range []struct {
		name, tenant, contact string
		want                  bool
	}{
		{"linked, own tenant by uuid", tenantA.String(), linked.String(), true},
		{"linked, own tenant by slug", "tenant-a", linked.String(), true},
		{"linked, asked by another known tenant", tenantB.String(), linked.String(), true},
		{"linked, upper-case contact id", tenantA.String(), strings.ToUpper(linked.String()), true},
		{"not linked", tenantA.String(), free.String(), false},
	} {
		code, body := askCRMLinks(r, tc.tenant, tc.contact, true)
		if code != http.StatusOK || body["referenced"] != tc.want {
			t.Errorf("%s: got %d %v, want 200 referenced=%v", tc.name, code, body, tc.want)
		}
		if len(body) != 1 {
			t.Errorf("%s: the answer must be the flag only, got %v", tc.name, body)
		}
	}

	if code, _ := askCRMLinks(r, "no-such-tenant", linked.String(), true); code != http.StatusNotFound {
		t.Errorf("unknown tenant slug: got %d, want 404", code)
	}
	if code, _ := askCRMLinks(r, uuid.NewString(), linked.String(), true); code != http.StatusNotFound {
		t.Errorf("unknown tenant uuid: got %d, want 404", code)
	}
	if code, _ := askCRMLinks(r, tenantA.String(), "not-a-uuid", true); code != http.StatusBadRequest {
		t.Errorf("bad contact id: got %d, want 400", code)
	}
	if code, _ := askCRMLinks(r, tenantA.String(), linked.String(), false); code != http.StatusUnauthorized {
		t.Errorf("no service key: got %d, want 401", code)
	}
}

// A failed database read or tenant lookup answers 500: marketflow then keeps the contact.
func TestCRMLinksFailuresAreNotNo(t *testing.T) {
	tenantID := uuid.New()
	h := &CRMLinksHandler{
		tenantID:   func(context.Context, string) (uuid.UUID, error) { return tenantID, nil },
		referenced: func(context.Context, uuid.UUID) (bool, error) { return false, errors.New("db down") },
		log:        zap.NewNop(),
	}
	if code, body := askCRMLinks(crmLinksRouter(h), tenantID.String(), uuid.NewString(), true); code != http.StatusInternalServerError || body["referenced"] != nil {
		t.Errorf("failed read: got %d %v, want 500 without a flag", code, body)
	}
	h.tenantID = func(context.Context, string) (uuid.UUID, error) { return uuid.Nil, errors.New("db down") }
	if code, _ := askCRMLinks(crmLinksRouter(h), tenantID.String(), uuid.NewString(), true); code != http.StatusInternalServerError {
		t.Errorf("failed tenant lookup: got %d, want 500", code)
	}
}
