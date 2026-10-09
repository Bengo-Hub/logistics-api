package zones

import (
	"encoding/json"
	"fmt"
)

// PolicyConfigKey is the ServiceConfig key that holds a tenant's delivery quote policy.
// A row with a nil tenant_id is the platform default.
const PolicyConfigKey = "logistics.delivery_quote_policy"

// Fallback modes for a point that is outside every delivery zone.
const (
	FallbackPerKm = "per_km"
	FallbackNone  = "none"

	DistanceRoad     = "road"
	DistanceStraight = "straight"
)

// Policy is the tenant's customer delivery pricing and geofence configuration. Zones
// carry area-specific fees; the policy decides what happens between and around them.
type Policy struct {
	// Fallback is per_km (charge by distance near the coverage) or none (zones only).
	Fallback string `json:"fallback"`
	// BufferKm accepts a pin outside every zone when it is within this distance of one.
	BufferKm float64 `json:"buffer_km"`
	// MaxRadiusKm optionally caps any delivery at this straight-line distance from the outlet.
	MaxRadiusKm *float64 `json:"max_radius_km,omitempty"`
	// RequireZones rejects every pin when the tenant has no active delivery zone. When
	// false, a tenant without zones is served by the per-km rule alone (within MaxRadiusKm).
	RequireZones bool `json:"require_zones"`

	BaseFee   float64 `json:"base_fee"`
	PerKmRate float64 `json:"per_km_rate"`
	MinFee    float64 `json:"min_fee"`
	// Rounding rounds fallback fees up to a multiple of this amount (0 = no rounding).
	Rounding float64 `json:"rounding"`

	DistanceSource     string  `json:"distance_source"`
	RoadFactorFallback float64 `json:"road_factor_fallback"`
	// SpeedKmh estimates the ETA for fallback quotes; PrepMinutes is added on top.
	SpeedKmh    float64 `json:"speed_kmh"`
	PrepMinutes int     `json:"prep_minutes"`

	Currency          string `json:"currency"`
	QuoteCacheSeconds int    `json:"quote_cache_seconds"`
}

// DefaultPolicy is used when neither the tenant nor the platform has stored one.
func DefaultPolicy() Policy {
	return Policy{
		Fallback:           FallbackPerKm,
		BufferKm:           2,
		RequireZones:       true,
		BaseFee:            0,
		PerKmRate:          50,
		MinFee:             100,
		Rounding:           10,
		DistanceSource:     DistanceRoad,
		RoadFactorFallback: 1.25,
		SpeedKmh:           25,
		PrepMinutes:        15,
		Currency:           "KES",
		QuoteCacheSeconds:  300,
	}
}

// ParsePolicy overlays a stored JSON policy onto the defaults so older rows that miss
// newer keys keep working.
func ParsePolicy(raw string) (Policy, error) {
	p := DefaultPolicy()
	if raw == "" {
		return p, nil
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return DefaultPolicy(), fmt.Errorf("zones: policy is not valid JSON: %w", err)
	}
	return p, p.Validate()
}

// Validate checks ranges and fills empty enums.
func (p *Policy) Validate() error {
	if p.Fallback == "" {
		p.Fallback = FallbackPerKm
	}
	if p.Fallback != FallbackPerKm && p.Fallback != FallbackNone {
		return fmt.Errorf("zones: fallback must be per_km or none")
	}
	if p.DistanceSource == "" {
		p.DistanceSource = DistanceRoad
	}
	if p.DistanceSource != DistanceRoad && p.DistanceSource != DistanceStraight {
		return fmt.Errorf("zones: distance_source must be road or straight")
	}
	if p.BufferKm < 0 || p.BufferKm > 200 {
		return fmt.Errorf("zones: buffer_km must be between 0 and 200")
	}
	if p.MaxRadiusKm != nil && (*p.MaxRadiusKm <= 0 || *p.MaxRadiusKm > 2000) {
		return fmt.Errorf("zones: max_radius_km must be between 0 and 2000")
	}
	if p.BaseFee < 0 || p.PerKmRate < 0 || p.MinFee < 0 || p.Rounding < 0 {
		return fmt.Errorf("zones: fees and rounding cannot be negative")
	}
	if p.RoadFactorFallback < 1 {
		p.RoadFactorFallback = 1.25
	}
	if p.SpeedKmh <= 0 {
		p.SpeedKmh = 25
	}
	if p.PrepMinutes < 0 {
		p.PrepMinutes = 0
	}
	if p.Currency == "" {
		p.Currency = "KES"
	}
	if p.QuoteCacheSeconds < 0 || p.QuoteCacheSeconds > 3600 {
		p.QuoteCacheSeconds = 300
	}
	return nil
}
