package zones

import (
	"context"

	"github.com/Bengo-Hub/httpware/money"
	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/modules/zones/geo"
)

// FeeSuggestion is the delivery fee the tenant's own rules give for a new area, measured
// from the dispatching outlet. The area form fills the fee from it, and the admin can still
// override it.
type FeeSuggestion struct {
	Outlet       *OutletPoint `json:"outlet,omitempty"`
	DistanceKm   float64      `json:"distance_km"`
	DistanceType string       `json:"distance_type,omitempty"`
	// NearKm and FarKm are the distances to the closest and furthest edge of the area,
	// so a wide area shows the price range it spans.
	NearKm float64 `json:"near_km"`
	FarKm  float64 `json:"far_km"`
	// SuggestedFee prices the centre of the area; NearFee and FarFee price its edges.
	SuggestedFee float64 `json:"suggested_fee"`
	NearFee      float64 `json:"near_fee"`
	FarFee       float64 `json:"far_fee"`
	// OutletInside is true when the outlet sits inside the area, where delivery is
	// normally free.
	OutletInside bool       `json:"outlet_inside"`
	Currency     string     `json:"currency"`
	Breakdown    *Breakdown `json:"breakdown,omitempty"`
	// Reason explains why there is no suggestion (no outlet with a map location yet).
	Reason string `json:"reason,omitempty"`
}

// SuggestFee prices an area centred on a point from the outlet it would be delivered from,
// with the tenant's per-km rules (base, rate, minimum, rounding) and distance source. The
// outlet is the requested one, else the nearest outlet with a map location. It reuses the
// same measure and perKmFee as a live quote, so a suggested fee matches what a pin at the
// centre would be charged by distance.
func (s *Service) SuggestFee(ctx context.Context, tenantID, outletID uuid.UUID, center geo.Point, radiusM float64) (FeeSuggestion, error) {
	snap, err := s.snapshot(ctx, tenantID)
	if err != nil {
		return FeeSuggestion{}, err
	}
	return suggestFee(snap, outletID, center, radiusM, s.distanceFunc(ctx)), nil
}

// suggestFee is the pure part of SuggestFee.
func suggestFee(snap snapshot, outletID uuid.UUID, center geo.Point, radiusM float64, distance DistanceFunc) FeeSuggestion {
	pol := snap.Policy
	out := FeeSuggestion{Currency: pol.Currency}
	outlet := pickOutlet(QuoteInput{Point: center, OutletID: outletID}, snap.Outlets)
	if outlet == nil || !geo.Valid(center) {
		out.Reason = ReasonNoOutlet
		return out
	}
	o := *outlet
	out.Outlet = &o

	radiusKm := radiusM / 1000
	km, kind := measure(outlet.Point, center, pol, distance)
	out.DistanceKm, out.DistanceType = km, kind
	out.NearKm = money.Round2(max(0, km-radiusKm))
	out.FarKm = money.Round2(km + radiusKm)
	out.SuggestedFee = perKmFee(pol, km)
	out.NearFee = perKmFee(pol, out.NearKm)
	out.FarFee = perKmFee(pol, out.FarKm)
	out.OutletInside = radiusM > 0 && geo.Distance(outlet.Point, center) <= radiusKm
	out.Breakdown = &Breakdown{
		BaseFee: pol.BaseFee, PerKmRate: pol.PerKmRate,
		Raw: money.Round2(pol.BaseFee + pol.PerKmRate*km), MinFee: pol.MinFee, Rounding: pol.Rounding,
	}
	return out
}
