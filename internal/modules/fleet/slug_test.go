package fleet

import "testing"

// A fleet slug that is a tenant id is a placeholder, never a routable slug.
func TestIsUUIDString(t *testing.T) {
	for _, s := range []string{"5bce71cd-a29f-484f-adc7-3566aed6d14f", " 33333333-3333-3333-3333-333333333333 "} {
		if !isUUIDString(s) {
			t.Errorf("%q should read as a tenant id placeholder", s)
		}
	}
	for _, s := range []string{"urban-loft", "codevertex-demo", ""} {
		if isUUIDString(s) {
			t.Errorf("%q is a real slug", s)
		}
	}
}
