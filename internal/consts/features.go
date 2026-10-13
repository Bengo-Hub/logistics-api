package consts

// Subscription feature codes logistics checks (subscriptions-api feature_catalog.go). Tier 1 runs
// deliveries; tier 2 adds what a delivery business needs to run a fleet; tier 3 adds the
// high-bandwidth features.
const (
	// Tier 2.
	FeatureAutoDispatch       = "auto_dispatch" // auto-assign new jobs and "nearest rider"
	FeatureRiderShifts        = "rider_shifts"  // the shift schedule (riders go online on any tier)
	FeatureRiderPayouts       = "rider_payouts" // earnings statements and payouts through treasury
	FeatureDriverAnalytics    = "driver_analytics"
	FeaturePerformanceReports = "performance_reports"
	// Tier 3.
	FeatureLiveTracking      = "live_tracking"
	FeatureRouteOptimisation = "route_optimisation"
	// Tier 1.
	FeatureRiderManagement = "rider_management"
)
