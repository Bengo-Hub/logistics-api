package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	ratelimit "github.com/Bengo-Hub/shared-ratelimit"
)

// secondsToNextMonth is how long until the monthly quota window resets (UTC).
func secondsToNextMonth(now time.Time) int {
	now = now.UTC()
	next := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	return int(next.Sub(now).Seconds())
}

// RequireRateLimit returns middleware that enforces a rate limit for a given feature.
// The limit is read from JWT claims via Claims.GetLimit(featureKey).
// upgradeURL is the subscription upgrade endpoint shown when limits are exceeded.
//
// Counting is shared-ratelimit's Quota (one atomic Lua call: increment, keep the 25h TTL, roll
// back on rejection); this wrapper only keeps logistics' response contract, which matches the
// shared limit-reached modal (code, metric, overage_eligible).
func RequireRateLimit(q *ratelimit.Quota, featureKey string, upgradeURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			// Gating-exempt tenants bypass metered limits entirely. SEC-3 (auth-client
			// v0.10.0): a tenant superuser is NOT exempt — only platform owner,
			// subscription-exempt, demo and service-charge tenants are.
			if claims.IsGatingExempt() {
				next.ServeHTTP(w, r)
				return
			}

			limit := claims.GetLimit(featureKey)
			if limit <= 0 { // 0 = absent/unlimited, -1 = explicit unlimited
				next.ServeHTTP(w, r)
				return
			}

			result, err := q.Check(r.Context(), claims.TenantID, featureKey, limit)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", result.Limit))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", result.Remaining))
			w.Header().Set("X-RateLimit-Feature", result.Feature)

			if !result.Allowed {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", fmt.Sprintf("%d", secondsToNextMonth(time.Now())))
				w.WriteHeader(http.StatusTooManyRequests)
				// Body matches the shared limit-reached modal contract (code, metric, limit,
				// used, overage_eligible). These metered metrics support pay-as-you-go overage.
				json.NewEncoder(w).Encode(map[string]any{
					"code":             "usage_limit_exceeded",
					"error":            "usage_limit_exceeded",
					"feature":          result.Feature,
					"metric":           result.Feature,
					"limit":            result.Limit,
					"used":             result.Used,
					"overage_eligible": true,
					"upgrade_url":      upgradeURL,
					"message":          fmt.Sprintf("Monthly %s limit reached. Upgrade your plan or enable extra usage.", featureKey),
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
