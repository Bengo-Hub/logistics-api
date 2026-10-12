package handlers

import "testing"

func TestValidatePayout(t *testing.T) {
	cases := []struct {
		name      string
		req       updatePayoutMethodRequest
		wantPhone string
		wantErr   bool
	}{
		{"mpesa local form", updatePayoutMethodRequest{PayoutMethod: "mpesa_b2c", PayoutPhone: "0712 345 678"}, "254712345678", false},
		{"mpesa with plus", updatePayoutMethodRequest{PayoutMethod: "mpesa_b2c", PayoutPhone: "+254712345678"}, "254712345678", false},
		{"mpesa missing phone", updatePayoutMethodRequest{PayoutMethod: "mpesa_b2c"}, "", true},
		{"mpesa junk phone", updatePayoutMethodRequest{PayoutMethod: "mpesa_b2c", PayoutPhone: "12"}, "", true},
		{"bank needs account", updatePayoutMethodRequest{PayoutMethod: "paystack_bank", PayoutBankCode: "01"}, "", true},
		{"bank complete", updatePayoutMethodRequest{PayoutMethod: "paystack_bank", PayoutBankCode: "01", AccountNumber: "123456"}, "", false},
		{"cash", updatePayoutMethodRequest{PayoutMethod: "cash"}, "", false},
		{"unknown method", updatePayoutMethodRequest{PayoutMethod: "crypto"}, "", true},
	}
	for _, c := range cases {
		phone, msg := validatePayout(c.req)
		if (msg != "") != c.wantErr {
			t.Errorf("%s: error %q, want error=%v", c.name, msg, c.wantErr)
		}
		if phone != c.wantPhone {
			t.Errorf("%s: phone %q, want %q", c.name, phone, c.wantPhone)
		}
	}
}
