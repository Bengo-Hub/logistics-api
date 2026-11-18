package middleware

import (
	"net/http"
	"strings"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

// AuthenticateWS authenticates a WebSocket or SSE handshake and, on success, returns a request
// carrying the resolved claims in context (via authclient.ContextWithClaims) so every
// downstream claims-reading middleware (TenantV2, RequireFeature, RequireRateLimit,
// RequirePermission) behaves exactly as it does for a normal authenticated REST request.
//
// Clients that can set headers send "Authorization: Bearer <token>". Browsers cannot set
// headers on a WebSocket or EventSource request, so they first fetch a single-use ticket from
// POST /api/v1/{tenant}/stream-ticket and connect with "?ticket=". An access token in the query
// string is never accepted: URLs are written to ingress and proxy access logs.
//
// On failure, writes the error response itself and returns ok=false. The caller must stop
// and not proceed to next.ServeHTTP.
func AuthenticateWS(validator *authclient.Validator, tickets *StreamTickets, w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		if validator == nil {
			http.Error(w, `{"error":"auth not configured"}`, http.StatusInternalServerError)
			return r, false
		}
		claims, err := validator.ValidateToken(strings.TrimSpace(h[7:]))
		if err != nil {
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return r, false
		}
		return r.WithContext(authclient.ContextWithClaims(r.Context(), claims)), true
	}

	ticket := r.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, `{"error":"missing stream ticket"}`, http.StatusUnauthorized)
		return r, false
	}
	if tickets == nil {
		http.Error(w, `{"error":"stream tickets not configured"}`, http.StatusInternalServerError)
		return r, false
	}
	claims, err := tickets.Consume(r.Context(), ticket)
	if err != nil {
		http.Error(w, `{"error":"invalid or expired stream ticket"}`, http.StatusUnauthorized)
		return r, false
	}
	return r.WithContext(authclient.ContextWithClaims(r.Context(), claims)), true
}

// IsStreamPath reports whether path is one of the WebSocket or SSE endpoints that authenticate
// through AuthenticateWS instead of the standard bearer middleware.
func IsStreamPath(path string) bool {
	return strings.HasSuffix(path, "/tracking/fleet/ws") ||
		strings.HasSuffix(path, "/notifications/stream") ||
		(strings.Contains(path, "/tasks/") && strings.HasSuffix(path, "/stream"))
}
