package zones

import (
	"math"
	"sort"
	"time"

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
	Serviceable   bool         `json:"serviceable"`
	Reason        string       `json:"reason,omitempty"`
	Method        string       `json:"method,omitempty"`
	Fee           float64      `json:"fee"`
	Free          bool         `json:"free"`
	Currency      string       `json:"currency"`
	Zone          *ZoneRef     `json:"zone,omitempty"`
	NearestArea   *ZoneRef     `json:"nearest_area,omitempty"`
	NearestAreaKm float64      `json:"nearest_area_km,omitempty"`
	DistanceKm    float64      `json:"distance_km"`
	DistanceType  string       `json:"distance_type,omitempty"`
	EtaMinutes    int          `json:"eta_minutes,omitempty"`
	MinOrder      float64      `json:"min_order"`
	BelowMinOrder bool         `json:"below_min_order,omitempty"`
	Outlet        *OutletPoint `json:"outlet,omitempty"`
	Breakdown     *Breakdown   `json:"breakdown,omitempty"`
	PolicyVersion string       `json:"policy_version"`
	CacheSeconds  int          `json:"cache_seconds"`
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
		q.NearestAreaKm = round2(nearestKm)
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
	} else if nearestKm > pol.BufferKm {
		q.Reason = ReasonOutside
		return q
	}
	if outlet == nil {
		q.Reason = ReasonNoOutlet
		return q
	}

	// 3. Per-km fallback near the coverage.
	km, kind := measure(outlet.Point, in.Point, pol, distance)
	raw := pol.BaseFee + pol.PerKmRate*km
	q.Serviceable = true
	q.Method = MethodPerKm
	q.Fee = perKmFee(pol, km)
	q.DistanceKm, q.DistanceType = km, kind
	q.EtaMinutes = estimateEta(km, pol)
	q.Breakdown = &Breakdown{BaseFee: pol.BaseFee, PerKmRate: pol.PerKmRate, Raw: round2(raw), MinFee: pol.MinFee, Rounding: pol.Rounding}
	applyMinOrder(&q, in.OrderTotal)
	return q
}

// perKmFee is the policy's distance price: base + rate x km, at least the minimum,
// rounded up to the rounding step.
func perKmFee(pol Policy, km float64) float64 {
	fee := math.Max(pol.BaseFee+pol.PerKmRate*km, pol.MinFee)
	if pol.Rounding > 0 {
		fee = math.Ceil(fee/pol.Rounding-1e-9) * pol.Rounding
	}
	return round2(fee)
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
			return round2(km), DistanceRoad
		}
	}
	straight := geo.Distance(from, to)
	if pol.DistanceSource == DistanceRoad {
		return round2(straight * pol.RoadFactorFallback), "estimated"
	}
	return round2(straight), DistanceStraight
}

func estimateEta(km float64, pol Policy) int {
	return pol.PrepMinutes + int(math.Ceil(km/pol.SpeedKmh*60))
}

func applyMinOrder(q *Quote, total float64) {
	if total > 0 && q.MinOrder > 0 && total < q.MinOrder {
		q.BelowMinOrder = true
	}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

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
