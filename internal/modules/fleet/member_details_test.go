package fleet

import (
	"errors"
	"testing"
)

func strp(s string) *string { return &s }

func TestMemberDetailsNormalize(t *testing.T) {
	d, err := MemberDetails{Phone: strp("0743 793 901")}.normalize()
	if err != nil || d.Phone == nil || *d.Phone != "+254743793901" {
		t.Fatalf("local number: got %v, %v", d.Phone, err)
	}
	d, err = MemberDetails{IDNumber: strp("  12345678 "), LicenseNo: strp(" DL1 ")}.normalize()
	if err != nil || *d.IDNumber != "12345678" || *d.LicenseNo != "DL1" {
		t.Fatalf("trimmed numbers: got %v %v, %v", d.IDNumber, d.LicenseNo, err)
	}
	for name, in := range map[string]MemberDetails{
		"empty phone": {Phone: strp(" ")},
		"bad phone":   {Phone: strp("12")},
		"empty ID":    {IDNumber: strp("")},
	} {
		if _, err := in.normalize(); !errors.Is(err, ErrInvalidMemberDetails) {
			t.Errorf("%s: want ErrInvalidMemberDetails, got %v", name, err)
		}
	}
	if d, err := (MemberDetails{}).normalize(); err != nil || d.Phone != nil {
		t.Errorf("nothing to change: got %v, %v", d, err)
	}
}
