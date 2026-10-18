package zones

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/Bengo-Hub/httpware/money"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/modules/zones/geo"
)

// Urban Loft Busia fixtures: outlet, free 3 km town circle, and a few named areas.
var (
	busiaOutlet = OutletPoint{ID: uuid.MustParse("458b9299-7eba-5a76-b036-a49487043497"), Name: "Urban Loft Cafe Busia", Point: geo.Point{Lat: 0.4545662, Lng: 34.1272854}}
	alupe       = geo.Point{Lat: 0.497094, Lng: 34.13416}
	malaba      = geo.Point{Lat: 0.632706, Lng: 34.26939}
	bulanda     = geo.Point{Lat: 0.452626, Lng: 34.10268} // inside the town circle
	kisumu      = geo.Point{Lat: -0.0917, Lng: 34.7680}
)

func circleZone(t *testing.T, name string, c geo.Point, radiusM, fee float64, free bool, priority int) compiledZone {
	t.Helper()
	s := ZoneSettings{Shape: ShapeCircle, Center: &c, RadiusM: radiusM, Fee: fee, Free: free, Priority: priority}
	ring, err := normalize(TypeDelivery, &s, nil)
	if err != nil {
		t.Fatalf("normalize %s: %v", name, err)
	}
	cz, ok := compile(&ent.GeoFence{ID: uuid.New(), Name: name, ZoneType: TypeDelivery, Status: StatusActive, Boundary: ring, Metadata: s.MergeIntoMetadata(nil), UpdatedAt: time.Now()})
	if !ok {
		t.Fatalf("compile %s", name)
	}
	return cz
}

func busiaSnapshot(t *testing.T) snapshot {
	return snapshot{
		Zones: []compiledZone{
			circleZone(t, "Busia Town", busiaOutlet.Point, 3000, 0, true, 10),
			circleZone(t, "Alupe", alupe, 1500, 100, false, 20),
			circleZone(t, "Malaba", malaba, 3000, 1050, false, 20),
			circleZone(t, "Bulanda", bulanda, 1000, 150, false, 20),
		},
		Outlets: []OutletPoint{busiaOutlet},
		Policy:  DefaultPolicy(),
		Version: "test",
	}
}

func straightOnly(from, to geo.Point) (float64, bool) { return 0, false }

func TestQuoteFreeTown(t *testing.T) {
	q := computeQuote(QuoteInput{Point: geo.Point{Lat: 0.46, Lng: 34.12}}, busiaSnapshot(t), straightOnly)
	if !q.Serviceable || q.Method != MethodZone || !q.Free || q.Fee != 0 || q.Zone.Name != "Busia Town" {
		t.Fatalf("town quote = %+v", q)
	}
}

func TestQuoteNamedAreas(t *testing.T) {
	snap := busiaSnapshot(t)
	for _, c := range []struct {
		p    geo.Point
		name string
		fee  float64
	}{{alupe, "Alupe", 100}, {malaba, "Malaba", 1050}} {
		q := computeQuote(QuoteInput{Point: c.p}, snap, straightOnly)
		if !q.Serviceable || q.Zone == nil || q.Zone.Name != c.name || q.Fee != c.fee || q.Free {
			t.Errorf("%s quote = %+v", c.name, q)
		}
	}
}

func TestQuoteNamedAreaInsideTownWinsByPriority(t *testing.T) {
	q := computeQuote(QuoteInput{Point: bulanda}, busiaSnapshot(t), straightOnly)
	if q.Zone == nil || q.Zone.Name != "Bulanda" || q.Fee != 150 {
		t.Fatalf("bulanda should be priced as a listed area, got %+v", q)
	}
}

func TestQuoteSamePrioritySmallestAreaWins(t *testing.T) {
	snap := busiaSnapshot(t)
	snap.Zones[3].Settings.Priority = 10 // Bulanda now ties with the town circle
	q := computeQuote(QuoteInput{Point: bulanda}, snap, straightOnly)
	if q.Zone.Name != "Bulanda" {
		t.Fatalf("smaller zone should win a priority tie, got %s", q.Zone.Name)
	}
}

func TestQuotePerKmInsideBuffer(t *testing.T) {
	// 1 km north of the Alupe circle edge (1.5 km radius), so 2.5 km from Alupe centre.
	p := geo.Point{Lat: alupe.Lat + 2.5/111.195, Lng: alupe.Lng}
	q := computeQuote(QuoteInput{Point: p}, busiaSnapshot(t), straightOnly)
	if !q.Serviceable || q.Method != MethodPerKm {
		t.Fatalf("expected per-km quote, got %+v", q)
	}
	if q.NearestArea == nil || q.NearestArea.Name != "Alupe" {
		t.Fatalf("nearest area = %+v", q.NearestArea)
	}
	// Road estimate = straight x 1.25; fee = 50/km rounded up to 10, at least 100.
	straight := geo.Distance(busiaOutlet.Point, p)
	want := math.Ceil(math.Max(50*money.Round2(straight*1.25), 100)/10) * 10
	if q.Fee != want || q.DistanceType != "estimated" {
		t.Fatalf("fee = %v (%s), want %v", q.Fee, q.DistanceType, want)
	}
	if math.Mod(q.Fee, 10) != 0 || q.Breakdown == nil {
		t.Fatal("fee must be rounded and carry a breakdown")
	}
}

func TestQuotePerKmUsesRoadDistanceAndMinimum(t *testing.T) {
	p := geo.Point{Lat: alupe.Lat + 2.5/111.195, Lng: alupe.Lng}
	road := func(from, to geo.Point) (float64, bool) { return 7.33, true }
	q := computeQuote(QuoteInput{Point: p}, busiaSnapshot(t), road)
	if q.Fee != 370 || q.DistanceKm != 7.33 || q.DistanceType != DistanceRoad {
		t.Fatalf("road quote = %+v", q)
	}
	short := func(from, to geo.Point) (float64, bool) { return 0.8, true }
	q = computeQuote(QuoteInput{Point: p}, busiaSnapshot(t), short)
	if q.Fee != 100 {
		t.Fatalf("minimum fee not applied: %v", q.Fee)
	}
}

func TestQuoteOutsideCoverage(t *testing.T) {
	q := computeQuote(QuoteInput{Point: kisumu}, busiaSnapshot(t), straightOnly)
	if q.Serviceable || q.Reason != ReasonOutside || q.NearestArea == nil {
		t.Fatalf("kisumu quote = %+v", q)
	}
	// 3 km past the Malaba edge is beyond the 2 km buffer (buffer coverage ignores reach).
	p := geo.Point{Lat: malaba.Lat + 6/111.195, Lng: malaba.Lng}
	snap := busiaSnapshot(t)
	snap.Policy.Coverage = CoverageBuffer
	if q := computeQuote(QuoteInput{Point: p}, snap, straightOnly); q.Serviceable {
		t.Fatalf("pin beyond buffer must be rejected: %+v", q)
	}
}

func TestQuoteFallbackNone(t *testing.T) {
	snap := busiaSnapshot(t)
	snap.Policy.Fallback = FallbackNone
	p := geo.Point{Lat: alupe.Lat + 2.5/111.195, Lng: alupe.Lng}
	if q := computeQuote(QuoteInput{Point: p}, snap, straightOnly); q.Serviceable || q.Reason != ReasonOutside {
		t.Fatalf("zones-only policy must reject, got %+v", q)
	}
}

func TestQuoteExclusionWins(t *testing.T) {
	snap := busiaSnapshot(t)
	c := geo.Point{Lat: 0.46, Lng: 34.12}
	s := ZoneSettings{Shape: ShapeCircle, Center: &c, RadiusM: 300}
	ring, _ := normalize(TypeExclusion, &s, nil)
	ex, _ := compile(&ent.GeoFence{ID: uuid.New(), Name: "Border yard", ZoneType: TypeExclusion, Status: StatusActive, Boundary: ring, Metadata: s.MergeIntoMetadata(nil)})
	snap.Zones = append(snap.Zones, ex)
	q := computeQuote(QuoteInput{Point: c}, snap, straightOnly)
	if q.Serviceable || q.Reason != ReasonExcluded {
		t.Fatalf("exclusion zone must reject, got %+v", q)
	}
}

func TestQuoteOutletScoping(t *testing.T) {
	snap := busiaSnapshot(t)
	other := OutletPoint{ID: uuid.New(), Name: "Other", Point: geo.Point{Lat: 0.30, Lng: 34.20}}
	snap.Outlets = append(snap.Outlets, other)
	snap.Zones[2].Settings.OutletIDs = []uuid.UUID{other.ID} // Malaba served only by the other outlet
	q := computeQuote(QuoteInput{Point: malaba, OutletID: busiaOutlet.ID}, snap, straightOnly)
	if q.Zone != nil && q.Zone.Name == "Malaba" {
		t.Fatalf("Malaba zone must not apply to the Busia outlet: %+v", q)
	}
	q = computeQuote(QuoteInput{Point: malaba, OutletID: other.ID}, snap, straightOnly)
	if q.Zone == nil || q.Zone.Name != "Malaba" {
		t.Fatalf("Malaba zone must apply to its outlet: %+v", q)
	}
}

func TestQuoteInactiveAndInvalid(t *testing.T) {
	snap := busiaSnapshot(t)
	snap.Zones[1].Status = StatusDraft
	q := computeQuote(QuoteInput{Point: alupe}, snap, straightOnly)
	if q.Zone != nil && q.Zone.Name == "Alupe" {
		t.Fatal("draft zone must not price")
	}
	if q := computeQuote(QuoteInput{Point: geo.Point{}}, snap, straightOnly); q.Reason != ReasonInvalidPoint {
		t.Fatalf("0,0 must be invalid, got %+v", q)
	}
}

func TestQuoteNoOutletAndNoZones(t *testing.T) {
	snap := busiaSnapshot(t)
	snap.Outlets = nil
	p := geo.Point{Lat: alupe.Lat + 2.5/111.195, Lng: alupe.Lng}
	if q := computeQuote(QuoteInput{Point: p}, snap, straightOnly); q.Reason != ReasonNoOutlet {
		t.Fatalf("per-km needs an outlet, got %+v", q)
	}
	empty := snapshot{Outlets: []OutletPoint{busiaOutlet}, Policy: DefaultPolicy()}
	if q := computeQuote(QuoteInput{Point: alupe}, empty, straightOnly); q.Reason != ReasonNoCoverage {
		t.Fatalf("require_zones must reject a tenant with no zones, got %+v", q)
	}
	empty.Policy.RequireZones = false
	max := 10.0
	empty.Policy.MaxRadiusKm = &max
	if q := computeQuote(QuoteInput{Point: alupe}, empty, straightOnly); !q.Serviceable || q.Method != MethodPerKm {
		t.Fatalf("distance-only tenant should quote per km, got %+v", q)
	}
	if q := computeQuote(QuoteInput{Point: malaba}, empty, straightOnly); q.Reason != ReasonBeyondRadius {
		t.Fatalf("max radius must apply, got %+v", q)
	}
}

func TestQuoteMinOrderFlag(t *testing.T) {
	snap := busiaSnapshot(t)
	snap.Zones[1].Settings.MinOrder = 500
	q := computeQuote(QuoteInput{Point: alupe, OrderTotal: 300}, snap, straightOnly)
	if !q.BelowMinOrder || q.MinOrder != 500 || !q.Serviceable {
		t.Fatalf("min order flag wrong: %+v", q)
	}
}

func TestNormalizeAndMetadataRoundTrip(t *testing.T) {
	keep := map[string]any{"source": "seed"}
	c := geo.Point{Lat: 0.5, Lng: 34.1}
	s := ZoneSettings{Shape: ShapeCircle, Center: &c, RadiusM: 1500, Fee: 120, Priority: 20, Aliases: []string{"Alupe Market"}, OutletIDs: []uuid.UUID{busiaOutlet.ID}}
	ring, err := normalize(TypeDelivery, &s, nil)
	if err != nil || len(ring) != circleSegments+1 {
		t.Fatalf("circle normalize: %v len=%d", err, len(ring))
	}
	md := s.MergeIntoMetadata(keep)
	if md["source"] != "seed" {
		t.Fatal("unrelated metadata must survive")
	}
	// Simulate a JSON round trip (numbers become float64, slices []any).
	back := SettingsFromMetadata(jsonRoundTrip(t, md))
	if back.Shape != ShapeCircle || back.RadiusM != 1500 || back.Fee != 120 || back.Priority != 20 ||
		len(back.Aliases) != 1 || len(back.OutletIDs) != 1 || back.Center == nil || back.Center.Lat != 0.5 {
		t.Fatalf("round trip = %+v", back)
	}

	bad := []struct {
		typ string
		s   ZoneSettings
		b   [][]float64
	}{
		{"teleport", ZoneSettings{Shape: ShapePolygon}, [][]float64{{1, 1}, {2, 1}, {2, 2}}},
		{TypeDelivery, ZoneSettings{Shape: ShapeCircle, RadiusM: 1000}, nil},
		{TypeDelivery, ZoneSettings{Shape: ShapeCircle, Center: &c, RadiusM: 10}, nil},
		{TypeDelivery, ZoneSettings{Shape: ShapePolygon}, [][]float64{{1, 1}, {2, 1}}},
		{TypeDelivery, ZoneSettings{Shape: ShapePolygon}, [][]float64{{1, 1}, {2, 1}, {200, 2}}},
		{TypeDelivery, ZoneSettings{Shape: ShapePolygon, Fee: -1}, [][]float64{{1, 1}, {2, 1}, {2, 2}}},
		{TypeDelivery, ZoneSettings{Shape: "hexagon"}, nil},
	}
	for i, b := range bad {
		s := b.s
		if _, err := normalize(b.typ, &s, b.b); err == nil {
			t.Errorf("case %d should fail", i)
		}
	}

	poly := ZoneSettings{Shape: ShapePolygon, Free: true, Fee: 50}
	ring, err = normalize(TypeDelivery, &poly, [][]float64{{34.1, 0.4}, {34.2, 0.4}, {34.2, 0.5}, {34.1, 0.5}})
	if err != nil || len(ring) != 5 || poly.Center == nil || poly.Fee != 0 {
		t.Fatalf("polygon normalize closes ring, derives centre and zeroes free fee: %v %v %+v", err, ring, poly)
	}
}

func TestPolicyParse(t *testing.T) {
	p, err := ParsePolicy(`{"per_km_rate": 60, "buffer_km": 3}`)
	if err != nil || p.PerKmRate != 60 || p.BufferKm != 3 || p.MinFee != 100 || p.Rounding != 10 {
		t.Fatalf("overlay on defaults failed: %+v %v", p, err)
	}
	if _, err := ParsePolicy(`{"fallback": "teleport"}`); err == nil {
		t.Fatal("bad fallback must fail")
	}
	if _, err := ParsePolicy(`{not json`); err == nil {
		t.Fatal("bad json must fail")
	}
}

func jsonRoundTrip(t *testing.T, in map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// south returns the point d km due south of the outlet (a meridian, so the great-circle
// distance is exactly d).
func south(d float64) geo.Point {
	return geo.Point{Lat: busiaOutlet.Point.Lat - d/111.195, Lng: busiaOutlet.Point.Lng}
}

func TestFurthestReachIsTheFarEdgeOfTheFurthestArea(t *testing.T) {
	snap := busiaSnapshot(t)
	var delivery []compiledZone
	delivery = append(delivery, snap.Zones...)
	km, far := furthestReach(busiaOutlet.Point, delivery)
	want := geo.Distance(busiaOutlet.Point, malaba) + 3
	if far.Name != "Malaba" || math.Abs(km-want) > 0.05 {
		t.Fatalf("reach = %.3f via %s, want about %.3f via Malaba", km, far.Name, want)
	}
}

func TestQuoteFurthestAreaServesPinsBetweenAreas(t *testing.T) {
	// 10 km south of the outlet: no area there and further than the 2 km buffer from every
	// one, but much closer than Malaba's far edge, so it is delivered by distance.
	p := south(10)
	q := computeQuote(QuoteInput{Point: p}, busiaSnapshot(t), straightOnly)
	if !q.Serviceable || q.Method != MethodPerKm || q.Zone != nil {
		t.Fatalf("pin inside the furthest reach must be delivered by distance, got %+v", q)
	}
	if q.Fee < 100 || math.Mod(q.Fee, 10) != 0 || q.Breakdown == nil {
		t.Fatalf("distance fee must follow the policy, got %+v", q)
	}
	if q.FurthestArea == nil || q.FurthestArea.Name != "Malaba" || q.FurthestAreaKm < 25 {
		t.Fatalf("quote should report the furthest area, got %+v", q.FurthestArea)
	}
	if !q.NeedsConfirmation {
		t.Fatal("distance-priced deliveries are confirmed with the customer by default")
	}
}

func TestQuoteBeyondFurthestAreaIsOutside(t *testing.T) {
	snap := busiaSnapshot(t)
	reach, _ := furthestReach(busiaOutlet.Point, snap.Zones)
	buf := snap.Policy.BufferKm

	if q := computeQuote(QuoteInput{Point: south(reach + buf - 0.05)}, snap, straightOnly); !q.Serviceable {
		t.Fatalf("just inside reach plus buffer must be served: %+v", q)
	}
	q := computeQuote(QuoteInput{Point: south(reach + buf + 0.05)}, snap, straightOnly)
	if q.Serviceable || q.Reason != ReasonOutside {
		t.Fatalf("beyond the furthest area must be out of the delivery area: %+v", q)
	}
	if q.FurthestArea == nil || q.FurthestAreaKm <= 0 || q.OutletKm < reach {
		t.Fatalf("the refusal must say how far deliveries go: %+v", q)
	}
}

func TestQuoteBufferCoverageIgnoresReach(t *testing.T) {
	snap := busiaSnapshot(t)
	snap.Policy.Coverage = CoverageBuffer
	if q := computeQuote(QuoteInput{Point: south(10)}, snap, straightOnly); q.Serviceable || q.Reason != ReasonOutside {
		t.Fatalf("buffer coverage must keep refusing pins far from every area: %+v", q)
	}
}

func TestQuoteConfirmationFlagFollowsPolicy(t *testing.T) {
	snap := busiaSnapshot(t)
	snap.Policy.ConfirmByEmail = false
	if q := computeQuote(QuoteInput{Point: south(10)}, snap, straightOnly); !q.Serviceable || q.NeedsConfirmation {
		t.Fatalf("confirmation switched off: %+v", q)
	}
	// An area-priced delivery never needs confirming, whatever the policy says.
	snap.Policy.ConfirmByEmail = true
	if q := computeQuote(QuoteInput{Point: alupe}, snap, straightOnly); q.NeedsConfirmation {
		t.Fatalf("area fees are published prices: %+v", q)
	}
}

func TestQuoteReachIsPerOutlet(t *testing.T) {
	snap := busiaSnapshot(t)
	other := OutletPoint{ID: uuid.New(), Name: "Other", Point: geo.Point{Lat: 0.30, Lng: 34.20}}
	snap.Outlets = append(snap.Outlets, other)
	// Every area is served only by the Busia outlet, so the other one has no reach at all.
	for i := range snap.Zones {
		snap.Zones[i].Settings.OutletIDs = []uuid.UUID{busiaOutlet.ID}
	}
	q := computeQuote(QuoteInput{Point: south(10), OutletID: other.ID}, snap, straightOnly)
	if q.Serviceable || q.Reason != ReasonNoCoverage && q.Reason != ReasonOutside {
		t.Fatalf("an outlet with no areas must not borrow another outlet's reach: %+v", q)
	}
}

func TestPolicyCoverageDefaultsAndValidation(t *testing.T) {
	p, err := ParsePolicy(`{"per_km_rate": 60}`)
	if err != nil || p.Coverage != CoverageFurthestArea || !p.ConfirmByEmail {
		t.Fatalf("older policies get the furthest-area rule and email confirmation: %+v %v", p, err)
	}
	p, err = ParsePolicy(`{"coverage": "buffer", "confirm_by_email": false}`)
	if err != nil || p.Coverage != CoverageBuffer || p.ConfirmByEmail {
		t.Fatalf("explicit values must stick: %+v %v", p, err)
	}
	if _, err := ParsePolicy(`{"coverage": "anywhere"}`); err == nil {
		t.Fatal("unknown coverage rule must fail")
	}
}

func TestSuggestFeeUsesTheTenantRulesFromTheOutlet(t *testing.T) {
	snap := busiaSnapshot(t)
	s := suggestFee(snap, uuid.Nil, alupe, 1500, straightOnly)
	km := money.Round2(geo.Distance(busiaOutlet.Point, alupe) * 1.25)
	want := math.Ceil(math.Max(50*km, 100)/10) * 10
	if s.Reason != "" || s.Outlet == nil || s.Outlet.ID != busiaOutlet.ID || s.SuggestedFee != want || s.DistanceKm != km {
		t.Fatalf("suggestion = %+v, want fee %v at %v km", s, want, km)
	}
	if s.NearKm >= s.DistanceKm || s.FarKm <= s.DistanceKm || s.NearFee > s.SuggestedFee || s.FarFee < s.SuggestedFee {
		t.Fatalf("an area spans a price range around its centre: %+v", s)
	}
	if s.OutletInside {
		t.Fatal("the outlet is not inside Alupe")
	}
	// A quote of the same centre by distance gives the same fee.
	q := computeQuote(QuoteInput{Point: south(10)}, snap, straightOnly)
	if got := suggestFee(snap, uuid.Nil, south(10), 500, straightOnly); got.SuggestedFee != q.Fee {
		t.Fatalf("suggestion %v must match the distance quote %v", got.SuggestedFee, q.Fee)
	}
}

func TestSuggestFeeOutletInsideAreaAndNoOutlet(t *testing.T) {
	snap := busiaSnapshot(t)
	if s := suggestFee(snap, busiaOutlet.ID, busiaOutlet.Point, 3000, straightOnly); !s.OutletInside || s.NearKm != 0 {
		t.Fatalf("a town circle around the outlet contains it: %+v", s)
	}
	snap.Outlets = nil
	if s := suggestFee(snap, uuid.Nil, alupe, 1500, straightOnly); s.Reason != ReasonNoOutlet || s.SuggestedFee != 0 {
		t.Fatalf("no outlet pin means no suggestion: %+v", s)
	}
}
