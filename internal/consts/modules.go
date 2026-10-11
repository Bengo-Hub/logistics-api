package consts

// Module keys — used for ServiceConfig logistics.enabled_modules and module gating.
const (
	ModuleDashboard    = "dashboard"
	ModuleFleet        = "fleet"
	ModuleDispatch     = "dispatch"
	ModuleTracking     = "tracking"
	ModuleAnalytics    = "analytics"
	ModuleEarnings     = "earnings"
	ModuleVehicles     = "vehicles"
	ModulePricing      = "pricing"
	ModuleDistribution = "distribution" // KEMSA / warehouse-to-warehouse
	ModuleColdChain    = "cold_chain"
	ModuleSmartLocks   = "smart_locks"
	ModuleRBAC         = "rbac"
	ModuleSettings     = "settings"
	ModuleReporting    = "reporting"
	ModuleShifts       = "shifts"
)

// AllModules is the superset of all known module keys.
var AllModules = []string{
	ModuleDashboard,
	ModuleFleet,
	ModuleDispatch,
	ModuleTracking,
	ModuleAnalytics,
	ModuleEarnings,
	ModuleVehicles,
	ModulePricing,
	ModuleDistribution,
	ModuleColdChain,
	ModuleSmartLocks,
	ModuleRBAC,
	ModuleSettings,
	ModuleReporting,
	ModuleShifts,
}

// deliveryModules is what a business that delivers its own orders needs (cafes, shops,
// pharmacies): riders, dispatch, delivery areas and pricing, rider pay, shifts, reports.
var deliveryModules = []string{
	ModuleDashboard, ModuleFleet, ModuleDispatch, ModuleTracking, ModuleAnalytics,
	ModuleEarnings, ModulePricing, ModuleVehicles, ModuleShifts, ModuleReporting,
	ModuleSettings, ModuleRBAC,
}

// withModules returns base plus extra, without duplicates.
func withModules(base []string, extra ...string) []string {
	out := append([]string{}, base...)
	for _, m := range extra {
		found := false
		for _, b := range out {
			if b == m {
				found = true
				break
			}
		}
		if !found {
			out = append(out, m)
		}
	}
	return out
}

// minimalModules is for use cases that do not deliver goods (ISP, library, property,
// weighbridge, FBO): logistics stays reachable, but only its frame.
var minimalModules = []string{ModuleDashboard, ModuleSettings, ModuleRBAC}

// UseCaseModules maps a tenant's use_case (auth-api usecase.KnownUseCases, plus the
// logistics-only courier/delivery/distribution) to its default modules. Resolution order:
// explicit ServiceConfig override, then this map, then AllModules for an unknown use case.
var UseCaseModules = map[string][]string{
	// Logistics businesses.
	"logistics":    withModules(deliveryModules, ModuleDistribution, ModuleColdChain, ModuleSmartLocks),
	"courier":      deliveryModules,
	"delivery":     deliveryModules,
	"distribution": withModules(deliveryModules, ModuleDistribution, ModuleColdChain, ModuleSmartLocks),

	// Businesses that deliver their own orders (ordering, POS till, inventory transfers).
	"hospitality":   deliveryModules,
	"quick_service": deliveryModules,
	"retail":        withModules(deliveryModules, ModuleDistribution),
	"pharmacy":      withModules(deliveryModules, ModuleDistribution, ModuleColdChain),
	"warehouse":     withModules(deliveryModules, ModuleDistribution),
	"manufacturing": withModules(deliveryModules, ModuleDistribution),
	// Hospitals dispatch ambulances and move stock between stores.
	"hospital": withModules(deliveryModules, ModuleDistribution, ModuleColdChain),

	// No goods delivery.
	"services":              minimalModules,
	"isp":                   minimalModules,
	"hotspot":               minimalModules,
	"property":              minimalModules,
	"library":               minimalModules,
	"fbo":                   minimalModules,
	"commercial_weighing":   minimalModules,
	"axle_load_enforcement": minimalModules,
}

// ResolveModules returns the enabled module list for a tenant.
// Priority: explicit override → use_case defaults → AllModules (backward-compat).
func ResolveModules(useCase string, override []string) []string {
	if len(override) > 0 {
		return override
	}
	if modules, ok := UseCaseModules[useCase]; ok {
		return modules
	}
	return AllModules
}

// DeliversGoods reports whether a use case runs deliveries, so logistics should mirror the
// tenant. Unknown or empty use cases count as delivering (fail open); ISP, library, property,
// weighbridge and similar tenants do not.
func DeliversGoods(useCase string) bool {
	modules, ok := UseCaseModules[useCase]
	if !ok {
		return true
	}
	for _, m := range modules {
		if m == ModuleDispatch {
			return true
		}
	}
	return false
}
