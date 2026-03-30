package handlers

import (
	"net/http"

	"github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// ResolveTenantForRequest resolves the target tenant UUID from the request,
// following the platform-owner override pattern:
//
//  1. Platform owners: check ?tenantId= query param (allows cross-tenant access)
//  2. httpware context (set by TenantV2 middleware from headers/JWT/URL)
//  3. URL path param {tenant}
//  4. JWT claims fallback
//
// Returns (uuid.Nil, true) when the caller is a platform owner and no specific
// tenant was requested — the handler should return data for ALL tenants.
// Returns (uuid.Nil, false) when tenant resolution fails entirely.
func ResolveTenantForRequest(r *http.Request) (uuid.UUID, bool) {
	ctx := r.Context()
	isPO := httpware.IsPlatformOwner(ctx)

	// 1. Platform owner query-param override
	if isPO {
		if q := r.URL.Query().Get("tenantId"); q != "" {
			if id, err := uuid.Parse(q); err == nil {
				return id, true
			}
		}
	}

	// 2. httpware context (from TenantV2 middleware)
	if tenantIDStr := httpware.GetTenantID(ctx); tenantIDStr != "" {
		if id, err := uuid.Parse(tenantIDStr); err == nil {
			if isPO {
				claims, ok := authclient.ClaimsFromContext(ctx)
				if ok && claims.TenantID == tenantIDStr {
					return uuid.Nil, true
				}
			}
			return id, true
		}
	}

	// 3. URL path parameter {tenant}
	if param := chi.URLParam(r, "tenant"); param != "" {
		if id, err := uuid.Parse(param); err == nil {
			return id, true
		}
	}

	// 4. JWT claims fallback (UUID tenant ID)
	claims, found := authclient.ClaimsFromContext(ctx)
	if found && claims.TenantID != "" {
		if id, err := uuid.Parse(claims.TenantID); err == nil {
			if isPO {
				return uuid.Nil, true
			}
			return id, true
		}
	}

	if isPO {
		return uuid.Nil, true
	}
	return uuid.Nil, false
}

// tenantIDFromClaims resolves tenant UUID using the shared platform-owner-aware
// resolver. Kept as a convenience wrapper to minimize changes in existing callers.
func tenantIDFromClaims(r *http.Request) uuid.UUID {
	id, ok := ResolveTenantForRequest(r)
	if !ok {
		return uuid.Nil
	}
	return id
}
