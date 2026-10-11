package consts

import "testing"

func TestResolveModulesByUseCase(t *testing.T) {
	has := func(ms []string, m string) bool {
		for _, x := range ms {
			if x == m {
				return true
			}
		}
		return false
	}
	if hos := ResolveModules("hospitality", nil); has(hos, ModuleDistribution) || has(hos, ModuleColdChain) || !has(hos, ModuleFleet) {
		t.Fatalf("a cafe delivers but runs no warehouse distribution: %v", hos)
	}
	if retail := ResolveModules("retail", nil); !has(retail, ModuleDistribution) {
		t.Fatal("retail moves stock between outlets")
	}
	if isp := ResolveModules("isp", nil); has(isp, ModuleFleet) || has(isp, ModuleDispatch) {
		t.Fatalf("an ISP delivers nothing: %v", isp)
	}
	if got := ResolveModules("unknown-case", nil); len(got) != len(AllModules) {
		t.Fatal("unknown use cases keep every module")
	}
	if got := ResolveModules("isp", []string{ModuleFleet}); len(got) != 1 || got[0] != ModuleFleet {
		t.Fatal("an explicit override wins")
	}
	for uc, ms := range UseCaseModules {
		seen := map[string]bool{}
		for _, m := range ms {
			if seen[m] {
				t.Fatalf("%s lists %s twice", uc, m)
			}
			seen[m] = true
		}
	}
}

func TestDeliversGoods(t *testing.T) {
	for _, uc := range []string{"hospitality", "retail", "logistics", "hospital", "", "brand-new"} {
		if !DeliversGoods(uc) {
			t.Errorf("%q should be mirrored", uc)
		}
	}
	for _, uc := range []string{"axle_load_enforcement", "isp", "library", "property", "services"} {
		if DeliversGoods(uc) {
			t.Errorf("%q does not deliver goods", uc)
		}
	}
}
