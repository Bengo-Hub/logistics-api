package router

import "strings"

// isPublicGuestRead reports whether a GET path is open to guests (customer checkout and
// public tracking) and so skips authentication. Only the customer-facing zone reads are
// public: the coverage map and the fee quote. The zone list, one zone and the zone
// analytics carry drafts, admin notes and delivery performance, so they need a sign-in.
// Routing (geocoding, ETA) and public tracking stay open.
func isPublicGuestRead(path string) bool {
	p := strings.TrimSuffix(path, "/")
	switch {
	case strings.HasSuffix(p, "/zones/coverage"), strings.HasSuffix(p, "/zones/quote"):
		return true
	case strings.Contains(path, "/routing/"), strings.Contains(path, "/track/"):
		return true
	}
	return false
}
