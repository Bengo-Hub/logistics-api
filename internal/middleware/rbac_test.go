package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
)

type denyAll struct{ calls int }

func (d *denyAll) HasPermission(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	d.calls++
	return false, nil
}

func TestRequirePermissionTenantAdminBypass(t *testing.T) {
	tenant := uuid.NewString()
	run := func(roles []string) (int, int) {
		checker := &denyAll{}
		h := RequirePermission(checker, "logistics.fleet.manage")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		claims := &authclient.Claims{TenantID: tenant, Roles: roles}
		claims.Subject = uuid.NewString()
		req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(authclient.ContextWithClaims(context.Background(), claims))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, checker.calls
	}
	if code, _ := run([]string{"admin"}); code != http.StatusNoContent {
		t.Fatalf("tenant admin: got %d, want 204", code)
	}
	if code, _ := run([]string{"superuser"}); code != http.StatusNoContent {
		t.Fatalf("superuser: got %d, want 204", code)
	}
	if code, calls := run([]string{"driver"}); code != http.StatusForbidden || calls != 1 {
		t.Fatalf("driver: got %d after %d checks, want 403 after 1", code, calls)
	}
}
