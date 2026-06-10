package middleware

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/redis/go-redis/v9"
)

// RateLimiter provides tenant-scoped rate limiting using Redis sliding window counters.
type RateLimiter struct {
	redis *redis.Client
}

// NewRateLimiter creates a new Redis-backed rate limiter.
func NewRateLimiter(rdb *redis.Client) *RateLimiter {
	return &RateLimiter{redis: rdb}
}

// LimitResult contains the result of a rate limit check.
type LimitResult struct {
	Allowed   bool   `json:"allowed"`
	Feature   string `json:"feature"`
	Limit     int    `json:"limit"`
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
}

// Check checks if a request is within the rate limit for a given tenant and feature.
// A limit of -1 means unlimited. A limit of 0 means feature not configured (allow by default).
func (rl *RateLimiter) Check(ctx context.Context, tenantID, feature string, limit int) (*LimitResult, error) {
	if limit < 0 {
		return &LimitResult{Allowed: true, Feature: feature, Limit: -1, Used: 0, Remaining: -1}, nil
	}
	if limit == 0 {
		return &LimitResult{Allowed: true, Feature: feature, Limit: 0, Used: 0, Remaining: 0}, nil
	}

	key := fmt.Sprintf("ratelimit:%s:%s:%s", tenantID, feature, time.Now().UTC().Format("2006-01-02"))

	count, err := rl.redis.Incr(ctx, key).Result()
	if err != nil {
		return &LimitResult{Allowed: true, Feature: feature, Limit: limit, Used: 0, Remaining: limit}, nil
	}

	if count == 1 {
		rl.redis.Expire(ctx, key, 25*time.Hour)
	}

	used := int(count)
	remaining := limit - used
	if remaining < 0 {
		remaining = 0
	}

	if used > limit {
		rl.redis.Decr(ctx, key)
		return &LimitResult{Allowed: false, Feature: feature, Limit: limit, Used: limit, Remaining: 0}, nil
	}

	return &LimitResult{Allowed: true, Feature: feature, Limit: limit, Used: used, Remaining: remaining}, nil
}

// RequireRateLimit returns middleware that enforces a rate limit for a given feature.
// The limit is read from JWT claims via Claims.GetLimit(featureKey).
// upgradeURL is the subscription upgrade endpoint shown when limits are exceeded.
func RequireRateLimit(rl *RateLimiter, featureKey string, upgradeURL string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := authclient.ClaimsFromContext(r.Context())
			if !ok {
				next.ServeHTTP(w, r)
				return
			}

			// Exempt tenants bypass metered limits entirely.
			if claims.IsPlatformOwner || claims.IsSuperuser() || claims.IsDemo || claims.BillingMode == "service_charge" {
				next.ServeHTTP(w, r)
				return
			}

			limit := claims.GetLimit(featureKey)
			if limit <= 0 { // 0 = absent/unlimited, -1 = explicit unlimited
				next.ServeHTTP(w, r)
				return
			}

			result, err := rl.Check(r.Context(), claims.TenantID, featureKey, limit)
			if err != nil {
				next.ServeHTTP(w, r)
				return
			}

			w.Header().Set("X-RateLimit-Limit", fmt.Sprintf("%d", result.Limit))
			w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", result.Remaining))
			w.Header().Set("X-RateLimit-Feature", result.Feature)

			if !result.Allowed {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "86400")
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
					"message":          fmt.Sprintf("Daily %s limit reached. Upgrade your plan or enable extra usage.", featureKey),
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
