package fleet

import (
	"errors"
	"testing"
	"time"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/shared/localtime"
)

func vehicle(vtype, status string, expiry *time.Time) *ent.Vehicle {
	return &ent.Vehicle{VehicleType: vtype, Status: status, InsuranceExpiry: expiry, LicensePlate: "KMGX040M"}
}

func TestVehicleBlock(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, localtime.Location)
	future, past := now.Add(48*time.Hour), now.Add(-time.Hour)
	block := InsurancePolicy{BlockUninsured: true, BlockExpired: true, ReminderDays: 7}
	allow := InsurancePolicy{ReminderDays: 7}

	cases := []struct {
		name string
		v    *ent.Vehicle
		p    InsurancePolicy
		want string
	}{
		{"no vehicle", nil, block, ""},
		{"insured motorbike", vehicle("motorcycle", "active", &future), block, ""},
		{"expired motorbike", vehicle("motorcycle", "active", &past), block, BlockInsuranceExpired},
		{"expired, tenant allows", vehicle("motorcycle", "active", &past), allow, ""},
		{"uninsured motorbike", vehicle("motorcycle", "active", nil), block, BlockNoInsurance},
		{"uninsured, tenant allows", vehicle("car", "active", nil), allow, ""},
		{"bicycle needs none", vehicle("bicycle", "active", nil), block, ""},
		{"expired bicycle policy is ignored", vehicle("bicycle", "active", &past), block, ""},
		{"maintenance blocks", vehicle("motorcycle", "maintenance", &future), allow, BlockVehicleInactive},
		{"deactivated blocks", vehicle("van", "inactive", &future), block, BlockVehicleInactive},
	}
	for _, c := range cases {
		if got := VehicleBlock(c.v, c.p, now); got != c.want {
			t.Errorf("%s: VehicleBlock = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestInsuranceStatus(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, localtime.Location)
	p := InsurancePolicy{BlockUninsured: true, BlockExpired: true, ReminderDays: 7}
	in := func(d int) *time.Time { t := localtime.EndOfDay(now.AddDate(0, 0, d)); return &t }

	for name, c := range map[string]struct {
		v    *ent.Vehicle
		want string
		days int
	}{
		"valid":        {vehicle("motorcycle", "active", in(30)), InsuranceValid, 30},
		"expiring":     {vehicle("motorcycle", "active", in(7)), InsuranceExpiring, 7},
		"last day":     {vehicle("motorcycle", "active", in(0)), InsuranceExpiring, 0},
		"expired":      {vehicle("motorcycle", "active", in(-1)), InsuranceExpired, -1},
		"missing":      {vehicle("motorcycle", "active", nil), InsuranceMissing, 0},
		"not required": {vehicle("bicycle", "active", nil), InsuranceNotRequired, 0},
	} {
		got := InsuranceStatusOf(c.v, p, now)
		if got.Status != c.want {
			t.Errorf("%s: status %q, want %q", name, got.Status, c.want)
		}
		if got.DaysLeft != nil && *got.DaysLeft != c.days {
			t.Errorf("%s: days left %d, want %d", name, *got.DaysLeft, c.days)
		}
	}
}

func TestInsuranceInputValidate(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, localtime.Location)
	ok := InsuranceInput{PolicyNo: " 030/071/1/000255/2026/03 ", StartsOn: "2026-09-09", ExpiresOn: "2027-03-24", Insurer: "Old Mutual"}
	vi, err := ok.validate(now, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if vi.ins.PolicyNo != "030/071/1/000255/2026/03" || localtime.Date(vi.expires) != "2027-03-24" || vi.expires.In(localtime.Location).Hour() != 23 {
		t.Fatalf("bad validated insurance %+v %v", vi.ins, vi.expires)
	}
	for name, in := range map[string]InsuranceInput{
		"no policy":       {ExpiresOn: "2027-03-24"},
		"bad date":        {PolicyNo: "P", ExpiresOn: "24/03/2027"},
		"already expired": {PolicyNo: "P", ExpiresOn: "2026-10-17"},
		"starts after":    {PolicyNo: "P", StartsOn: "2027-04-01", ExpiresOn: "2027-03-24"},
	} {
		if _, err := in.validate(now, ""); !errors.Is(err, ErrInvalidInsurance) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	// Expiring today is still a valid (last-day) policy.
	if _, err := (InsuranceInput{PolicyNo: "P", ExpiresOn: "2026-10-18"}).validate(now, ""); err != nil {
		t.Errorf("a policy expiring today must be accepted: %v", err)
	}
}
