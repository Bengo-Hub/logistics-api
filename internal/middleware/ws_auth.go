package middleware

import (
	"net/http"
	"strings"

	authclient "github.com/Bengo-Hub/shared-auth-client"
)

// AuthenticateWS validates a WebSocket handshake request and, on success, returns a request
// carrying the resolved claims in context (via authclient.ContextWithClaims) so every
// downstream claims-reading middleware (TenantV2, RequireFeature, RequireRateLimit) behaves
// exactly as it does for a normal authenticated REST request.
//
// A browser's native WebSocket API cannot set an Authorization header on the handshake
// request, so this checks the header first (for any client that can set it) and falls back
// to a "?token=" query parameter — a standard, well-established pattern for this exact
// constraint. The token is still fully verified via the same validator every Bearer token
// goes through; this is not a weaker trust-the-caller shortcut.
//
// On failure, writes the error response itself and returns ok=false — the caller must stop
// and not proceed to next.ServeHTTP.
func AuthenticateWS(validator *authclient.Validator, w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	token := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(h), "bearer ") {
		token = strings.TrimSpace(h[7:])
	} else if q := r.URL.Query().Get("token"); q != "" {
		token = q
	}

	if token == "" {
		http.Error(w, `{"error":"missing bearer token"}`, http.StatusUnauthorized)
		return r, false
	}

	claims, err := validator.ValidateToken(token)
	if err != nil {
		http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
		return r, false
	}

	return r.WithContext(authclient.ContextWithClaims(r.Context(), claims)), true
}
