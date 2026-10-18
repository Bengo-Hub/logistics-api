package router

import "testing"

func TestPublicGuestReads(t *testing.T) {
	public := []string{
		"/api/v1/urban-loft/zones/coverage",
		"/api/v1/urban-loft/zones/coverage/",
		"/api/v1/urban-loft/zones/quote",
		"/api/v1/urban-loft/routing/geocode/search",
		"/api/v1/track/ABC123",
	}
	for _, p := range public {
		if !isPublicGuestRead(p) {
			t.Errorf("%s must stay public for guest checkout", p)
		}
	}
	// These used to be public because the old rule matched any path containing "/zones".
	private := []string{
		"/api/v1/urban-loft/zones",
		"/api/v1/urban-loft/zones/",
		"/api/v1/urban-loft/zones/6f1d8a52-0000-4000-8000-000000000000",
		"/api/v1/urban-loft/analytics/zones",
		"/api/v1/urban-loft/delivery-policy",
		"/api/v1/urban-loft/delivery-policy/suggest-fee",
		"/api/v1/urban-loft/tasks",
	}
	for _, p := range private {
		if isPublicGuestRead(p) {
			t.Errorf("%s must require authentication", p)
		}
	}
}
