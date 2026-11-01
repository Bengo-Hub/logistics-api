package identity

import (
	"testing"

	"github.com/bengobox/logistics-service/internal/ent"
)

func TestKYCChanged(t *testing.T) {
	fm := &ent.FleetMember{IDNumber: "12345678", LicenseNo: "DL1", IDPassportAttachment: "kyc/id.jpg", RiderPhoto: "kyc/me.jpg"}
	cases := []struct {
		name string
		req  UpdateRiderProfileRequest
		want bool
	}{
		{"phone only", UpdateRiderProfileRequest{Phone: "+254700000000"}, false},
		{"same details re-sent", UpdateRiderProfileRequest{IDNumber: "12345678", LicenseNo: "DL1", IDPassportAttachment: "kyc/id.jpg", RiderPhoto: "kyc/me.jpg"}, false},
		{"new ID number", UpdateRiderProfileRequest{IDNumber: "87654321"}, true},
		{"new licence", UpdateRiderProfileRequest{LicenseNo: "DL2"}, true},
		{"new ID document", UpdateRiderProfileRequest{IDPassportAttachment: "kyc/id2.jpg"}, true},
		{"new photo", UpdateRiderProfileRequest{RiderPhoto: "kyc/me2.jpg"}, true},
	}
	for _, c := range cases {
		if got := kycChanged(fm, c.req); got != c.want {
			t.Errorf("%s: kycChanged = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNeedsKYCReview(t *testing.T) {
	cases := []struct {
		status  string
		changed bool
		want    bool
	}{
		{"active", false, false},
		{"active", true, true},
		{"suspended", false, false},
		{"suspended", true, false},
		{"invited", false, true},
		{"pending", false, true},
		{"pending_review", false, true},
		{"rejected", false, true},
	}
	for _, c := range cases {
		if got := needsKYCReview(c.status, c.changed); got != c.want {
			t.Errorf("needsKYCReview(%q, %v) = %v, want %v", c.status, c.changed, got, c.want)
		}
	}
}
