package fleet

import (
	"testing"

	"github.com/google/uuid"
)

func validOnboard() OnboardRiderRequest {
	return OnboardRiderRequest{
		Email: " Jane@Example.com ", FirstName: " Jane ", Phone: "0700000000", IDNumber: " 12345678 ",
	}
}

func TestOnboardRequestValidate(t *testing.T) {
	r := validOnboard()
	if err := r.validate(); err != nil {
		t.Fatalf("valid request refused: %v", err)
	}
	if r.Email != "jane@example.com" || r.FirstName != "Jane" || r.IDNumber != "12345678" {
		t.Fatalf("request not trimmed: %+v", r)
	}

	// The ID number is the one identity detail required; uploads are optional.
	for name, mut := range map[string]func(*OnboardRiderRequest){
		"no first name": func(r *OnboardRiderRequest) { r.FirstName = " " },
		"bad email":     func(r *OnboardRiderRequest) { r.Email = "jane" },
		"no phone":      func(r *OnboardRiderRequest) { r.Phone = "" },
		"no id number":  func(r *OnboardRiderRequest) { r.IDNumber = "  " },
		"both vehicles": func(r *OnboardRiderRequest) {
			id := uuid.New()
			r.VehicleID = &id
			r.Vehicle = &CreateVehicleRequest{VehicleType: "bike", LicensePlate: "KMFA 123X"}
		},
	} {
		r := validOnboard()
		mut(&r)
		if err := r.validate(); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestVehicleNormalisation(t *testing.T) {
	for in, want := range map[string]string{"bike": "motorcycle", " Motorbike ": "motorcycle", "motorcycle": "motorcycle", "Van": "van", "bicycle": "bicycle"} {
		if got, err := NormalizeVehicleType(in); err != nil || got != want {
			t.Errorf("NormalizeVehicleType(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := NormalizeVehicleType("hovercraft"); err == nil {
		t.Error("unknown vehicle type accepted")
	}
	if got := NormalizePlate("  kmfa   123x "); got != "KMFA 123X" {
		t.Errorf("NormalizePlate = %q", got)
	}
}
