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
	// For a pin outside every area: RouteArea is the delivery area whose route from the
	// outlet passes closest to the pin, RouteReachKm how far that route goes (outlet to the
	// area's far edge, straight line), RouteOffKm how far the pin is from that route, and
	// Direction the compass direction of the pin from the outlet. OutletKm is the pin's
	// straight-line distance from the outlet.
	RouteArea    *ZoneRef `json:"route_area,omitempty"`
	RouteReachKm float64  `json:"route_reach_km,omitempty"`
	RouteOffKm   float64  `json:"route_off_km,omitempty"`
	Direction    string   `json:"direction,omitempty"`
	OutletKm     float64  `json:"outlet_km,omitempty"`
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
	// Adjusted says the distance price was held to the fees of the areas on the same route:
	// "capped" at the furthest one's fee or "raised" to the nearest passed one's. AdjustedTo
	// is the resulting fee and AdjustedBy the area whose fee set it.
	Adjusted   string  `json:"adjusted,omitempty"`
	AdjustedTo float64 `json:"adjusted_to,omitempty"`
	AdjustedBy string  `json:"adjusted_by,omitempty"`
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

	// Each delivery area is a route from the outlet to its far edge. A pin close to one of
	// those routes (a small deviation, same road or surroundings) is on the way to somewhere
	// the tenant already serves; a pin in the opposite direction is not, however near.
	var offRouteKm float64
	if outlet != nil && len(delivery) > 0 {
		r, off := nearestRoute(outlet.Point, in.Point, delivery)
		offRouteKm = off
		q.RouteArea = &ZoneRef{ID: r.Zone.ID, Name: r.Zone.Name}
		q.RouteReachKm = money.Round2(r.ReachKm)
		q.RouteOffKm = money.Round2(off)
		q.OutletKm = money.Round2(geo.Distance(outlet.Point, in.Point))
		q.Direction = geo.Compass(geo.Bearing(outlet.Point, in.Point))
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
	} else if nearestKm > pol.BufferKm && !onRoute(pol, outlet, offRouteKm) {
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
	// Hold the price to the published fees of the areas on the same route.
	if pol.Coverage == CoverageRoute {
		fee, how, by := holdToRoute(q.Fee, routesNear(outlet.Point, in.Point, delivery, pol.CorridorKm), geo.Distance(outlet.Point, in.Point))
		if how != "" {
			q.Fee = fee
			q.Breakdown.Adjusted, q.Breakdown.AdjustedTo, q.Breakdown.AdjustedBy = how, fee, by
		}
	}
	applyMinOrder(&q, in.OrderTotal)
	return q
}

// routesNear lists the routes whose corridor the pin falls in: the delivery areas on the
// way to (or just short of, or just past) the pin.
func routesNear(from, p geo.Point, zs []compiledZone, corridorKm float64) []route {
	var out []route
	for i := range zs {
		r := routeTo(from, &zs[i])
		if r.ReachKm >= 0 && geo.DistanceToSegmentKm(p, from, r.Far) <= corridorKm {
			out = append(out, r)
		}
	}
	return out
}

// holdToRoute keeps a distance price consistent with the published area fees on the same
// route. It never exceeds the fee of the furthest area on the route (a pin past it costs no
// more than that area), and never undercuts the fee of the furthest area already passed on
// the way to the pin (further out costs at least as much as a nearer area). Free areas set
// no bound, and the cap wins over the floor. Returns the fee, "capped" or "raised" ("" when
// unchanged), and the area whose fee set it.
func holdToRoute(fee float64, routes []route, pinKm float64) (float64, string, string) {
	var furthest, passed *route
	for i := range routes {
		z := routes[i].Zone
		if z.Settings.Free || z.Settings.Fee <= 0 {
			continue
		}
		if furthest == nil || routes[i].ReachKm > furthest.ReachKm {
			furthest = &routes[i]
		}
		if routes[i].ReachKm <= pinKm && (passed == nil || routes[i].ReachKm > passed.ReachKm) {
			passed = &routes[i]
		}
	}
	if furthest != nil && fee > furthest.Zone.Settings.Fee {
		return furthest.Zone.Settings.Fee, "capped", furthest.Zone.Name
	}
	// An odd tariff where a nearer area costs more than the furthest one must not push the
	// price above the furthest area's fee.
	if passed != nil && fee < passed.Zone.Settings.Fee && (furthest == nil || passed.Zone.Settings.Fee <= furthest.Zone.Settings.Fee) {
		return passed.Zone.Settings.Fee, "raised", passed.Zone.Name
	}
	return fee, "", ""
}

// route is the straight line from the outlet to the far edge of one delivery area.
type route struct {
	Zone    *compiledZone
	Far     geo.Point // the boundary point furthest from the outlet
	ReachKm float64   // outlet to Far
}

// routeTo is the route to a zone: the far point is the boundary vertex furthest from the
// outlet. Distance to a point is convex, so the maximum over a polygon is at a vertex (a
// circle is stored as a 48-point polygon).
func routeTo(from geo.Point, z *compiledZone) route {
	r := route{Zone: z, ReachKm: -1}
	for _, c := range z.Ring {
		if len(c) < 2 {
			continue
		}
		p := geo.Point{Lat: c[1], Lng: c[0]}
		if d := geo.Distance(from, p); d > r.ReachKm {
			r.ReachKm, r.Far = d, p
		}
	}
	return r
}

// nearestRoute finds the delivery route that passes closest to a pin and how far (km) the
// pin is from it. A pin beyond a route's end is measured to that end, so past the furthest
// area it quickly leaves every corridor, and a pin in the opposite direction is far from
// every route whatever its distance from the outlet. zs must not be empty.
func nearestRoute(from, p geo.Point, zs []compiledZone) (route, float64) {
	var best route
	bestOff := math.Inf(1)
	for i := range zs {
		r := routeTo(from, &zs[i])
		if r.ReachKm < 0 {
			continue
		}
		if off := geo.DistanceToSegmentKm(p, from, r.Far); off < bestOff {
			best, bestOff = r, off
		}
	}
	return best, bestOff
}

// onRoute reports whether a pin outside every area is still served under the policy's
// coverage rule: within CorridorKm of the route to some area. The buffer-only rule never
// serves on routes, and without an outlet pin there is nothing to measure from.
func onRoute(pol Policy, outlet *OutletPoint, offRouteKm float64) bool {
	if pol.Coverage != CoverageRoute || outlet == nil {
		return false
	}
	return offRouteKm <= pol.CorridorKm
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
