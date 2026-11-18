package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/http/handlers"
)

var (
	homeTenant = uuid.MustParse("11111111-1111-4111-8111-111111111111")
	urbanLoft  = uuid.MustParse("22222222-2222-4222-8222-222222222222")
)

// tenantChain mirrors the production order: claims, the JIT middleware writing the caller's home
// tenant, TenantV2 on /{tenant}, then resolveURLTenant. The handler reports what
// ResolveTenantForRequest picks.
func tenantChain(claims *authclient.Claims, resolve slugResolver) http.Handler {
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := authclient.ContextWithClaims(req.Context(), claims)
			ctx = httpware.WithTenantID(ctx, claims.TenantID)
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/{tenant}", func(t chi.Router) {
		t.Use(httpware.TenantV2(httpware.TenantConfig{
			ClaimsExtractor: func(ctx context.Context) (string, string, bool, bool) {
				c, ok := authclient.ClaimsFromContext(ctx)
				if !ok {
					return "", "", false, false
				}
				return c.TenantID, c.GetTenantSlug(), c.IsPlatformOwner, true
			},
			URLParamFunc: chi.URLParam,
			Required:     true,
		}))
		t.Use(resolveURLTenant(resolve))
		t.Get("/probe", func(w http.ResponseWriter, req *http.Request) {
			id, ok := handlers.ResolveTenantForRequest(req)
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(id.String()))
		})
	})
	return r
}

func slugs(m map[string]uuid.UUID) slugResolver {
	return func(_ context.Context, slug string) (uuid.UUID, error) {
		if id, ok := m[slug]; ok {
			return id, nil
		}
		return uuid.Nil, errors.New("unknown tenant")
	}
}

func probe(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestURLTenantWinsForPlatformOwner(t *testing.T) {
	owner := &authclient.Claims{TenantID: homeTenant.String(), TenantSlug: "codevertex", IsPlatformOwner: true}
	h := tenantChain(owner, slugs(map[string]uuid.UUID{"codevertex": homeTenant, "urban-loft": urbanLoft}))

	rec := probe(h, "/urban-loft/probe")
	if rec.Code != http.StatusOK || rec.Body.String() != urbanLoft.String() {
		t.Fatalf("owner on urban-loft got %d %q, want %s", rec.Code, rec.Body.String(), urbanLoft)
	}

	// An unknown slug must not quietly fall back to the owner's home tenant.
	if rec := probe(h, "/no-such-tenant/probe"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown slug got %d, want 404", rec.Code)
	}
}

// A platform owner signed in through the tenant's own app has a token issued for that tenant. The URL
// names the same tenant, and the handler must get it, not "all tenants" (uuid.Nil answered 401 on
// tasks, riders and tracking for urban-loft, 18 November 2026).
func TestPlatformOwnerTokenForURLTenant(t *testing.T) {
	owner := &authclient.Claims{TenantID: urbanLoft.String(), TenantSlug: "urban-loft", IsPlatformOwner: true}
	h := tenantChain(owner, slugs(map[string]uuid.UUID{"codevertex": homeTenant, "urban-loft": urbanLoft}))
	rec := probe(h, "/urban-loft/probe")
	if rec.Code != http.StatusOK || rec.Body.String() != urbanLoft.String() {
		t.Fatalf("owner with an urban-loft token got %d %q, want %s", rec.Code, rec.Body.String(), urbanLoft)
	}
}

func TestTenantUserStaysPinnedToToken(t *testing.T) {
	user := &authclient.Claims{TenantID: urbanLoft.String(), TenantSlug: "urban-loft"}
	h := tenantChain(user, slugs(map[string]uuid.UUID{"urban-loft": urbanLoft, "codevertex": homeTenant}))

	rec := probe(h, "/urban-loft/probe")
	if rec.Code != http.StatusOK || rec.Body.String() != urbanLoft.String() {
		t.Fatalf("own tenant got %d %q", rec.Code, rec.Body.String())
	}
	if rec := probe(h, "/codevertex/probe"); rec.Code != http.StatusForbidden {
		t.Fatalf("foreign tenant got %d, want 403 from TenantV2", rec.Code)
	}
}
