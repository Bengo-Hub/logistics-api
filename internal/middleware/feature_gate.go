package middleware

import (
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

// RequireFeature returns middleware that blocks requests when the tenant's subscription
// does not include featureCode. It now delegates to the canonical
// authclient.RequireFeatureCode, which funnels exemption through the shared
// claims.IsGatingExempt() (platform owner, demo, service-charge, sub-exempt) so the
// semantics match pos/ordering/inventory/treasury — tenant superusers are NOT exempt and
// must be on a plan that includes the feature. When no claims are present (e.g. a public
// path), the request is passed through so the outer auth layer can decide.
//
// The upgradeURL parameter is retained for call-site compatibility but is now unused; the
// canonical middleware emits a uniform 403 body with the platform-standard upgrade_url.
//
// Apply it on premium logistics route groups (live tracking, analytics, fleet management).
// It is deliberately NOT applied to the public routing/ETA endpoints used by guest
// checkout, nor to the core task dispatch path that order fulfilment depends on.
func RequireFeature(featureCode, upgradeURL string) func(http.Handler) http.Handler {
	return authclient.RequireFeatureCode(featureCode)
}

// RequireAnyFeature is like RequireFeature but passes when the subscription includes ANY
// of featureCodes (used where two feature codes both unlock the same surface, e.g.
// driver_analytics OR performance_reports for the analytics dashboards). It delegates to
// the canonical authclient.RequireAnyFeatureCode.
func RequireAnyFeature(upgradeURL string, featureCodes ...string) func(http.Handler) http.Handler {
	return authclient.RequireAnyFeatureCode(featureCodes...)
}
