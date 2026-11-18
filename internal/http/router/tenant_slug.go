package router

import (
	"context"
	"net/http"
	"strings"

	"github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
)

// slugResolver turns a tenant slug into the local tenant UUID (syncing it from auth when new).
type slugResolver func(ctx context.Context, slug string) (uuid.UUID, error)

// resolveURLTenant runs after TenantV2 and makes sure the tenant UUID in context matches the
// tenant the URL names.
//
// Two cases need it. A fresh DB where TenantV2 only had a slug. And a platform owner on another
// tenant's URL: TenantV2 keeps the URL slug but drops its own id, so the home tenant id the JIT
// middleware stored earlier would still answer GetTenantID and every handler would act on the
// owner's home tenant (or on all tenants). A non-owner can never get here with a foreign slug,
// because TenantV2 already refused the mismatch with a 403.
func resolveURLTenant(resolve slugResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			slug := httpware.GetTenantSlug(ctx)
			if slug == "" {
				next.ServeHTTP(w, r)
				return
			}
			foreign := false
			if claims, ok := authclient.ClaimsFromContext(ctx); ok && claims.GetTenantSlug() != "" {
				foreign = !strings.EqualFold(slug, claims.GetTenantSlug())
			}
			if httpware.GetTenantID(ctx) != "" && !foreign {
				next.ServeHTTP(w, r)
				return
			}
			tid, err := resolve(ctx, slug)
			if err != nil || tid == uuid.Nil {
				if foreign {
					// Never fall back to the caller's home tenant for a URL naming another one.
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"error":"tenant not found","code":"tenant_not_found"}`))
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(httpware.WithTenantID(ctx, tid.String())))
		})
	}
}
