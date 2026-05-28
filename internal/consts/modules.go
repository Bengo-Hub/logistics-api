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

// UseCaseModules maps a tenant's use_case to its default enabled module set.
// Resolution order: explicit ServiceConfig override → use_case defaults → AllModules.
var UseCaseModules = map[string][]string{
	"courier": {
		ModuleDashboard, ModuleFleet, ModuleDispatch, ModuleTracking,
		ModuleAnalytics, ModuleEarnings, ModuleVehicles, ModuleSettings,
		ModuleRBAC, ModuleShifts,
	},
	"delivery": {
		ModuleDashboard, ModuleFleet, ModuleDispatch, ModuleTracking,
		ModuleAnalytics, ModuleEarnings, ModulePricing, ModuleSettings,
		ModuleRBAC,
	},
	"distribution": {
		ModuleDashboard, ModuleFleet, ModuleDispatch, ModuleTracking,
		ModuleDistribution, ModuleColdChain, ModuleSmartLocks,
		ModuleAnalytics, ModuleEarnings, ModuleVehicles, ModuleSettings,
		ModuleRBAC, ModuleShifts, ModuleReporting,
	},
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
