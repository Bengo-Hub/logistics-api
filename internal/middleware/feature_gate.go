package middleware

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

// RequireFeature returns middleware that blocks requests when the tenant's subscription
// does not include featureCode. Exemption funnels through the shared
// claims.IsGatingExempt() (platform owner, demo, service-charge, sub-exempt) so the
// semantics match pos/ordering/inventory/treasury — tenant superusers are NOT exempt and
// must be on a plan that includes the feature. When no claims are present (e.g. a public
// path), the request is passed through so the outer auth layer can decide.
//
// Apply it on premium logistics route groups (live tracking, analytics, fleet management).
// It is deliberately NOT applied to the public routing/ETA endpoints used by guest
// checkout, nor to the core task dispatch path that order fulfilment depends on.
func RequireFeature(featureCode, upgradeURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if claims.IsGatingExempt() {
				next.ServeHTTP(w, r)
				return
			}
			if !claims.HasFeature(featureCode) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code":             "feature_not_available", // canonical discriminator read by frontends
					"error":            "feature_not_available",
					"required_feature": featureCode,
					"upgrade":          true,
					"upgrade_url":      upgradeURL,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireAnyFeature is like RequireFeature but passes when the subscription includes ANY
// of featureCodes (used where two feature codes both unlock the same surface, e.g.
// driver_analytics OR performance_reports for the analytics dashboards).
func RequireAnyFeature(upgradeURL string, featureCodes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if claims.IsGatingExempt() || claims.HasAnyFeature(featureCodes...) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			required := ""
			if len(featureCodes) > 0 {
				required = featureCodes[0]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code":             "feature_not_available", // canonical discriminator read by frontends
				"error":            "feature_not_available",
				"required_feature": required,
				"upgrade":          true,
				"upgrade_url":      upgradeURL,
			})
		})
	}
}
