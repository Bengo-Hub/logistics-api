package subscriptions

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

const (
	// LimitRiders is the plan-limit key (in the JWT) for the fleet head-count cap.
	LimitRiders = "max_riders"

	upgradeURL = "/settings?tab=subscription"
)

// exempt reports whether the request's token bypasses all subscription gating
// (platform owners, explicitly subscription-exempt tenants, demo tenants, and
// service-charge tenants). Mirrors pos-api's platform/subscriptions/gate.go so gating
// behaves identically fleet-wide.
func exempt(r *http.Request) bool {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		return true // no claims (e.g. S2S/pin paths) — don't block here
	}
	return claims.IsGatingExempt()
}

// CheckStructuralLimit enforces a hard-block structural cap (riders, vehicles, …) before
// creating a new resource. Returns true when the request may proceed, or writes a
// structured 402 and returns false when the cap is reached.
func CheckStructuralLimit(w http.ResponseWriter, r *http.Request, metric, limitKey string, currentCount int) bool {
	if exempt(r) {
		return true
	}
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		return true
	}
	limit := claims.GetLimit(limitKey)
	if limit <= 0 {
		return true // 0 = not configured, -1 = unlimited
	}
	if currentCount >= limit {
		writeLimitReached(w, metric, limit, currentCount)
		return false
	}
	return true
}

// writeLimitReached emits the structured 402 the frontend's LimitReachedModal consumes
// (same wire contract as pos-api/inventory-api's structural-limit gates).
func writeLimitReached(w http.ResponseWriter, metric string, limit, used int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusPaymentRequired)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":             "usage_limit_exceeded",
		"error":            "usage_limit_exceeded",
		"message":          "You've reached your plan's " + metric + " limit.",
		"metric":           metric,
		"limit":            limit,
		"used":             used,
		"overage_eligible": false,
		"upgrade_url":      upgradeURL,
	})
}
