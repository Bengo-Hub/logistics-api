package middleware

import (
	"context"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/Bengo-Hub/httpware"
	"github.com/google/uuid"
)

// PermissionChecker is the interface the RBAC middleware requires from the RBAC service.
type PermissionChecker interface {
	HasPermission(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, permissionCode string) (bool, error)
}

// IsTenantAdmin reports whether the token carries the tenant admin or superuser role.
func IsTenantAdmin(claims *authclient.Claims) bool {
	return claims != nil && (claims.IsSuperuser() || claims.IsAdmin())
}

// RequirePermission returns a middleware that rejects requests where the authenticated
// user does not hold the given permission in their tenant.
// Platform owners bypass the check.
func RequirePermission(svc PermissionChecker, permissionCode string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()

			// Platform owners always pass
			if httpware.IsPlatformOwner(ctx) {
				next.ServeHTTP(w, r)
				return
			}

			claims, ok := authclient.ClaimsFromContext(ctx)
			if !ok || claims.Subject == "" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			// Tenant admins hold every logistics permission in their own tenant. Auth issues
			// the admin/superuser role per tenant, so no logistics role assignment is needed.
			if IsTenantAdmin(claims) {
				next.ServeHTTP(w, r)
				return
			}

			userID, err := uuid.Parse(claims.Subject)
			if err != nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			tenantIDStr := httpware.GetTenantID(ctx)
			if tenantIDStr == "" {
				tenantIDStr = claims.TenantID
			}
			tenantID, err := uuid.Parse(tenantIDStr)
			if err != nil {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}

			allowed, err := svc.HasPermission(ctx, tenantID, userID, permissionCode)
			if err != nil || !allowed {
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
