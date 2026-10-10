package erp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestRaiseClaim(t *testing.T) {
	tenant := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != "k" || r.Header.Get("X-Tenant-ID") != tenant.String() {
			t.Errorf("missing S2S headers")
		}
		var req ClaimRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.SourceKey {
		case "ok":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"c1","amount":"4000.00","status":"pending"}`))
		case "short":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"trip is shorter than the per diem distance threshold","code":"not_eligible"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", 0)
	ctx := context.Background()

	claim, err := c.RaiseClaim(ctx, tenant, ClaimRequest{SourceKey: "ok"})
	if err != nil || claim.ID != "c1" || claim.Amount != "4000.00" {
		t.Fatalf("created claim: %+v %v", claim, err)
	}
	_, err = c.RaiseClaim(ctx, tenant, ClaimRequest{SourceKey: "short"})
	if rej, ok := IsRejection(err); !ok || rej.Code != "not_eligible" {
		t.Fatalf("422 must be a rejection with erp's code, got %v", err)
	}
	_, err = c.RaiseClaim(ctx, tenant, ClaimRequest{SourceKey: "boom"})
	if _, ok := IsRejection(err); ok || err == nil {
		t.Fatalf("5xx must be a plain error, got %v", err)
	}
	if NewClient("", "", 0).Enabled() {
		t.Fatal("empty config must be disabled")
	}
}
