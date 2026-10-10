package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
)

type fixedChecker bool

func (f fixedChecker) HasPermission(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	return bool(f), nil
}

func TestRequirePermissionDelegatesToService(t *testing.T) {
	run := func(allow bool) int {
		h := RequirePermission(fixedChecker(allow), "logistics.fleet.manage")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))
		claims := &authclient.Claims{TenantID: uuid.NewString()}
		claims.Subject = uuid.NewString()
		req := httptest.NewRequest(http.MethodPost, "/", nil).WithContext(authclient.ContextWithClaims(context.Background(), claims))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := run(true); got != http.StatusNoContent {
		t.Fatalf("allowed: got %d", got)
	}
	if got := run(false); got != http.StatusForbidden {
		t.Fatalf("denied: got %d", got)
	}
}
