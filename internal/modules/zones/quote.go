package zones

import (
	"math"
	"sort"
	"time"

	"github.com/Bengo-Hub/httpware/money"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/modules/zones/geo"
)

// Quote methods and not-serviceable reasons returned to every consumer.
const (
	MethodZone  = "zone"
	MethodPerKm = "per_km"

	ReasonExcluded      = "excluded_area"
	ReasonOutside       = "outside_delivery_area"
	ReasonBeyondRadius  = "beyond_max_radius"
	ReasonNoOutlet      = "no_outlet_location"
	ReasonNoCoverage    = "no_delivery_coverage"
	ReasonInvalidPoint  = "invalid_location"
	ReasonBelowMinOrder = "below_min_order"
)

// OutletPoint is an outlet that can dispatch deliveries.
type OutletPoint struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Point geo.Point `json:"location"`
}

// QuoteInput is one delivery pricing request.
type QuoteInput struct {
	Point      geo.Point
	OutletID   uuid.UUID // optional; nearest outlet with a location is used when nil
	OrderTotal float64   // optional; used to flag min_order shortfalls
}

// ZoneRef identifies the zone a quote matched or the nearest one.
type ZoneRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

// Quote is the answer every consumer receives. Fee is authoritative: callers must not
// recompute it.
type Quote struct {
	Serviceable   bool     `json:"serviceable"`
	Reason        string   `json:"reason,omitempty"`
	Method        string   `json:"method,omitempty"`
	Fee           float64  `json:"fee"`
	Free          bool     `json:"free"`
	Currency      string   `json:"currency"`
	Zone          *ZoneRef `json:"zone,omitempty"`
	NearestArea   *ZoneRef `json:"nearest_area,omitempty"`
	NearestAreaKm float64  `json:"nearest_area_km,omitempty"`
	// FurthestArea is the area reaching furthest from the outlet and FurthestAreaKm how far
	// its far edge is (straight line). Returned for pins outside every area so callers can
	// say how far deliveries go. OutletKm is the straight-line distance of the pin.
	FurthestArea   *ZoneRef `json:"furthest_area,omitempty"`
	FurthestAreaKm float64  `json:"furthest_area_km,omitempty"`
	OutletKm       float64  `json:"outlet_km,omitempty"`
	// NeedsConfirmation marks a distance-priced delivery whose area, distance and fee the
	// customer is sent to confirm with the order confirmation (tenant policy).
	NeedsConfirmation bool         `json:"needs_confirmation,omitempty"`
	DistanceKm        float64      `json:"distance_km"`
	DistanceType      string       `json:"distance_type,omitempty"`
	EtaMinutes        int          `json:"eta_minutes,omitempty"`
	MinOrder          float64      `json:"min_order"`
	BelowMinOrder     bool         `json:"below_min_order,omitempty"`
	Outlet            *OutletPoint `json:"outlet,omitempty"`
	Breakdown         *Breakdown   `json:"breakdown,omitempty"`
	PolicyVersion     string       `json:"policy_version"`
	CacheSeconds      int          `json:"cache_seconds"`
}

// Breakdown explains a per-km fee so admins and customers can see how it was built.
type Breakdown struct {
	BaseFee   float64 `json:"base_fee"`
	PerKmRate float64 `json:"per_km_rate"`
	Raw       float64 `json:"raw"`
	MinFee    float64 `json:"min_fee"`
	Rounding  float64 `json:"rounding"`
}

// DistanceFunc returns the travel distance in km between two points and whether it is
// a road distance (true) or an estimate (false).
type DistanceFunc func(from, to geo.Point) (km float64, road bool)

// snapshot is everything a quote needs for one tenant, loaded once and cached.
type snapshot struct {
	Zones   []compiledZone
	Outlets []OutletPoint
	Policy  Policy
	Version string
}

// computeQuote is the single delivery pricing rule for the platform. It is pure: all
// I/O (zones, outlets, policy, road distance) is supplied by the caller.
func computeQuote(in QuoteInput, snap snapshot, distance DistanceFunc) Quote {
	pol := snap.Policy
	q := Quote{Currency: pol.Currency, PolicyVersion: snap.Version, CacheSeconds: pol.QuoteCacheSeconds}
	if !geo.Valid(in.Point) {
		q.Reason = ReasonInvalidPoint
		return q
	}

	outlet := pickOutlet(in, snap.Outlets)
	if outlet != nil {
		o := *outlet
		q.Outlet = &o
	}

	var delivery, exclusion []compiledZone
	for _, z := range snap.Zones {
		if z.Status != StatusActive {
			continue
		}
		oid := uuid.Nil
		if outlet != nil {
			oid = outlet.ID
		}
		if !z.servesOutlet(oid) {
			continue
		}
		switch z.Type {
		case TypeDelivery:
			delivery = append(delivery, z)
		case TypeExclusion:
			exclusion = append(exclusion, z)
		}
	}

	for _, z := range exclusion {
		if z.contains(in.Point) {
			q.Reason = ReasonExcluded
			q.Zone = &ZoneRef{ID: z.ID, Name: z.Name}
			return q
		}
	}

	if outlet != nil && pol.MaxRadiusKm != nil {
		if geo.Distance(outlet.Point, in.Point) > *pol.MaxRadiusKm {
			q.Reason = ReasonBeyondRadius
			return q
		}
	}

	// 1. The most specific matching zone wins: highest priority, then smallest area.
	var hits []compiledZone
	for _, z := range delivery {
		if z.contains(in.Point) {
			hits = append(hits, z)
		}
	}
	if len(hits) > 0 {
		sort.SliceStable(hits, func(i, j int) bool {
			if hits[i].Settings.Priority != hits[j].Settings.Priority {
				return hits[i].Settings.Priority > hits[j].Settings.Priority
			}
			return hits[i].AreaKm2 < hits[j].AreaKm2
		})
		z := hits[0]
		q.Serviceable = true
		q.Method = MethodZone
		q.Zone = &ZoneRef{ID: z.ID, Name: z.Name}
		q.Free = z.Settings.Free || z.Settings.Fee == 0
		if !q.Free {
			q.Fee = z.Settings.Fee
		}
		if z.Settings.Currency != "" {
			q.Currency = z.Settings.Currency
		}
		q.MinOrder = z.Settings.MinOrder
		if outlet != nil {
			q.DistanceKm, q.DistanceType = measure(outlet.Point, in.Point, pol, distance)
		}
		q.EtaMinutes = z.Settings.EtaMinutes
		if q.EtaMinutes == 0 {
			q.EtaMinutes = estimateEta(q.DistanceKm, pol)
		}
		applyMinOrder(&q, in.OrderTotal)
		return q
	}

	// 2. Outside every zone: find the nearest area for messaging and the buffer check.
	nearestKm := math.Inf(1)
	var nearest *compiledZone
	for i := range delivery {
		d := geo.DistanceToRingKm(in.Point, delivery[i].Ring)
		if d < nearestKm {
			nearestKm, nearest = d, &delivery[i]
		}
	}
	if nearest != nil {
		q.NearestArea = &ZoneRef{ID: nearest.ID, Name: nearest.Name}
		q.NearestAreaKm = money.Round2(nearestKm)
	}

	// The furthest area from this outlet tells how far deliveries go: a pin between the
	// outlet and that edge is closer than somewhere the tenant already serves.
	var reachKm float64
	if outlet != nil && len(delivery) > 0 {
		var far *compiledZone
		reachKm, far = furthestReach(outlet.Point, delivery)
		q.FurthestArea = &ZoneRef{ID: far.ID, Name: far.Name}
		q.FurthestAreaKm = money.Round2(reachKm)
		q.OutletKm = money.Round2(geo.Distance(outlet.Point, in.Point))
	}

	if pol.Fallback != FallbackPerKm {
		q.Reason = ReasonOutside
		return q
	}
	if nearest == nil {
		if pol.RequireZones {
			q.Reason = ReasonNoCoverage
			return q
		}
	} else if nearestKm > pol.BufferKm && !withinReach(pol, outlet, in.Point, reachKm) {
		q.Reason = ReasonOutside
		return q
	}
	if outlet == nil {
		q.Reason = ReasonNoOutlet
		return q
	}

	// 3. Per-km price for a pin outside the areas but inside the coverage.
	km, kind := measure(outlet.Point, in.Point, pol, distance)
	raw := pol.BaseFee + pol.PerKmRate*km
	q.Serviceable = true
	q.Method = MethodPerKm
	q.Fee = perKmFee(pol, km)
	q.DistanceKm, q.DistanceType = km, kind
	q.OutletKm = money.Round2(geo.Distance(outlet.Point, in.Point))
	q.EtaMinutes = estimateEta(km, pol)
	q.NeedsConfirmation = pol.ConfirmByEmail
	q.Breakdown = &Breakdown{BaseFee: pol.BaseFee, PerKmRate: pol.PerKmRate, Raw: money.Round2(raw), MinFee: pol.MinFee, Rounding: pol.Rounding}
	applyMinOrder(&q, in.OrderTotal)
	return q
}

// furthestReach is how far from the outlet the delivery areas extend: the largest
// straight-line distance from the outlet to any boundary point of an area, and the area
// that reaches that far. The distance to a point is convex, so the maximum over a
// polygon is always at one of its vertices (a circle is stored as a 48-point polygon).
// zs must not be empty.
func furthestReach(from geo.Point, zs []compiledZone) (float64, *compiledZone) {
	var best *compiledZone
	bestKm := -1.0
	for i := range zs {
		for _, c := range zs[i].Ring {
			if len(c) < 2 {
				continue
			}
			if d := geo.Distance(from, geo.Point{Lat: c[1], Lng: c[0]}); d > bestKm {
				bestKm, best = d, &zs[i]
			}
		}
	}
	return bestKm, best
}

// withinReach reports whether a pin outside every area is still served under the
// policy's coverage rule: closer to the outlet than the furthest area's far edge (plus the
// buffer slack). The buffer-only rule never serves on reach, and without an outlet pin
// there is nothing to measure from.
func withinReach(pol Policy, outlet *OutletPoint, p geo.Point, reachKm float64) bool {
	if pol.Coverage != CoverageFurthestArea || outlet == nil || reachKm <= 0 {
		return false
	}
	return geo.Distance(outlet.Point, p) <= reachKm+pol.BufferKm
}

// perKmFee is the policy's distance price: base + rate x km, at least the minimum,
// rounded up to the rounding step.
func perKmFee(pol Policy, km float64) float64 {
	fee := math.Max(pol.BaseFee+pol.PerKmRate*km, pol.MinFee)
	if pol.Rounding > 0 {
		fee = math.Ceil(fee/pol.Rounding-1e-9) * pol.Rounding
	}
	return money.Round2(fee)
}

func pickOutlet(in QuoteInput, outlets []OutletPoint) *OutletPoint {
	if in.OutletID != uuid.Nil {
		for i := range outlets {
			if outlets[i].ID == in.OutletID {
				return &outlets[i]
			}
		}
	}
	var best *OutletPoint
	bestKm := math.Inf(1)
	for i := range outlets {
		if d := geo.Distance(outlets[i].Point, in.Point); d < bestKm {
			bestKm, best = d, &outlets[i]
		}
	}
	return best
}

func measure(from, to geo.Point, pol Policy, distance DistanceFunc) (float64, string) {
	if pol.DistanceSource == DistanceRoad && distance != nil {
		if km, road := distance(from, to); road && km > 0 {
			return money.Round2(km), DistanceRoad
		}
	}
	straight := geo.Distance(from, to)
	if pol.DistanceSource == DistanceRoad {
		return money.Round2(straight * pol.RoadFactorFallback), "estimated"
	}
	return money.Round2(straight), DistanceStraight
}

func estimateEta(km float64, pol Policy) int {
	return pol.PrepMinutes + int(math.Ceil(km/pol.SpeedKmh*60))
}

func applyMinOrder(q *Quote, total float64) {
	if total > 0 && q.MinOrder > 0 && total < q.MinOrder {
		q.BelowMinOrder = true
	}
}

// versionOf fingerprints the latest change across zones and the policy row so callers
// can store which configuration priced an order.
func versionOf(zs []compiledZone, policyUpdated time.Time) string {
	latest := policyUpdated
	for _, z := range zs {
		if z.UpdatedAt.After(latest) {
			latest = z.UpdatedAt
		}
	}
	if latest.IsZero() {
		return "default"
	}
	return latest.UTC().Format("20060102T150405Z")
}
