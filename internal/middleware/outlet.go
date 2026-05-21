package middleware

import (
	"net/http"

	"github.com/Bengo-Hub/httpware"
)

// OutletContext extracts the X-Outlet-ID header and stores it in the request context.
// This is optional — requests without the header pass through unchanged.
func OutletContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if outletID := httpware.OutletHeader(r); outletID != "" {
			r = r.WithContext(httpware.WithOutletID(r.Context(), outletID))
		}
		next.ServeHTTP(w, r)
	})
}
