package zones

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/modules/zones/geo"
)

// Zone types. A delivery zone prices and accepts drop-offs, an exclusion zone is a
// no-go area that is rejected even when it sits inside a delivery zone.
const (
	TypeDelivery  = "delivery"
	TypeExclusion = "exclusion"
	TypePickup    = "pickup"
	TypeSurge     = "surge"

	ShapeCircle  = "circle"
	ShapePolygon = "polygon"

	StatusActive   = "active"
	StatusInactive = "inactive"
	StatusDraft    = "draft"

	circleSegments = 48
)

var validTypes = map[string]bool{TypeDelivery: true, TypeExclusion: true, TypePickup: true, TypeSurge: true}
var validStatuses = map[string]bool{StatusActive: true, StatusInactive: true, StatusDraft: true}

// ZoneSettings is the delivery contract stored in GeoFence.metadata. It is the single
// definition of what a service area means for every consumer (ordering, pos, couriers,
// distribution tenants, rider-app).
type ZoneSettings struct {
	Shape      string      `json:"shape"`
	Center     *geo.Point  `json:"center,omitempty"`
	RadiusM    float64     `json:"radius_m,omitempty"`
	Fee        float64     `json:"fee"`
	Free       bool        `json:"free"`
	Currency   string      `json:"currency,omitempty"`
	MinOrder   float64     `json:"min_order"`
	EtaMinutes int         `json:"eta_minutes,omitempty"`
	Priority   int         `json:"priority"`
	OutletIDs  []uuid.UUID `json:"outlet_ids,omitempty"`
	Aliases    []string    `json:"aliases,omitempty"`
	Notes      string      `json:"notes,omitempty"`
}

// settingsKeys are the metadata keys owned by ZoneSettings; any other metadata keys a
// client stores are preserved untouched.
var settingsKeys = []string{"shape", "center", "radius_m", "fee", "free", "currency", "min_order", "eta_minutes", "priority", "outlet_ids", "aliases", "notes"}

// SettingsFromMetadata decodes the contract from a zone's metadata map. Missing values
// fall back to zero values; a zone without a shape is treated as a polygon.
func SettingsFromMetadata(md map[string]any) ZoneSettings {
	s := ZoneSettings{Shape: ShapePolygon}
	if md == nil {
		return s
	}
	if v, ok := md["shape"].(string); ok && v != "" {
		s.Shape = v
	}
	if c, ok := md["center"].(map[string]any); ok {
		lat, okLat := toFloat(c["lat"])
		lng, okLng := toFloat(c["lng"])
		if okLat && okLng {
			s.Center = &geo.Point{Lat: lat, Lng: lng}
		}
	}
	s.RadiusM, _ = toFloat(md["radius_m"])
	s.Fee, _ = toFloat(md["fee"])
	s.Free, _ = md["free"].(bool)
	s.Currency, _ = md["currency"].(string)
	s.MinOrder, _ = toFloat(md["min_order"])
	if v, ok := toFloat(md["eta_minutes"]); ok {
		s.EtaMinutes = int(v)
	}
	if v, ok := toFloat(md["priority"]); ok {
		s.Priority = int(v)
	}
	if arr, ok := md["outlet_ids"].([]any); ok {
		for _, x := range arr {
			if str, ok := x.(string); ok {
				if id, err := uuid.Parse(str); err == nil {
					s.OutletIDs = append(s.OutletIDs, id)
				}
			}
		}
	}
	if arr, ok := md["aliases"].([]any); ok {
		for _, x := range arr {
			if str, ok := x.(string); ok && strings.TrimSpace(str) != "" {
				s.Aliases = append(s.Aliases, strings.TrimSpace(str))
			}
		}
	}
	s.Notes, _ = md["notes"].(string)
	return s
}

// MergeIntoMetadata writes the contract into md, keeping unrelated keys.
func (s ZoneSettings) MergeIntoMetadata(md map[string]any) map[string]any {
	out := make(map[string]any, len(md)+len(settingsKeys))
	for k, v := range md {
		out[k] = v
	}
	for _, k := range settingsKeys {
		delete(out, k)
	}
	out["shape"] = s.Shape
	if s.Center != nil {
		out["center"] = map[string]any{"lat": s.Center.Lat, "lng": s.Center.Lng}
	}
	if s.Shape == ShapeCircle {
		out["radius_m"] = s.RadiusM
	}
	out["fee"] = s.Fee
	out["free"] = s.Free
	if s.Currency != "" {
		out["currency"] = s.Currency
	}
	out["min_order"] = s.MinOrder
	if s.EtaMinutes > 0 {
		out["eta_minutes"] = s.EtaMinutes
	}
	out["priority"] = s.Priority
	if len(s.OutletIDs) > 0 {
		ids := make([]any, len(s.OutletIDs))
		for i, id := range s.OutletIDs {
			ids[i] = id.String()
		}
		out["outlet_ids"] = ids
	}
	if len(s.Aliases) > 0 {
		al := make([]any, len(s.Aliases))
		for i, a := range s.Aliases {
			al[i] = a
		}
		out["aliases"] = al
	}
	if s.Notes != "" {
		out["notes"] = s.Notes
	}
	return out
}

// normalize validates the settings and returns the boundary that must be stored. For a
// circle the polygon is generated from centre and radius; for a polygon the supplied
// boundary is checked and closed, and the centre is derived when absent.
func normalize(zoneType string, s *ZoneSettings, boundary [][]float64) ([][]float64, error) {
	if !validTypes[zoneType] {
		return nil, fmt.Errorf("zones: zone_type must be one of delivery, exclusion, pickup, surge")
	}
	if s.Fee < 0 || s.MinOrder < 0 {
		return nil, fmt.Errorf("zones: fee and min_order cannot be negative")
	}
	if s.Free {
		s.Fee = 0
	}
	if s.EtaMinutes < 0 {
		s.EtaMinutes = 0
	}
	switch s.Shape {
	case ShapeCircle:
		if s.Center == nil || !geo.Valid(*s.Center) {
			return nil, fmt.Errorf("zones: a circle needs a valid centre latitude and longitude")
		}
		if s.RadiusM < 50 || s.RadiusM > 500_000 {
			return nil, fmt.Errorf("zones: radius must be between 50 m and 500 km")
		}
		return geo.CirclePolygon(*s.Center, s.RadiusM, circleSegments), nil
	case ShapePolygon, "":
		s.Shape = ShapePolygon
		s.RadiusM = 0
		ring := make([][]float64, 0, len(boundary)+1)
		for _, c := range boundary {
			if len(c) < 2 {
				return nil, fmt.Errorf("zones: every boundary point needs [lng, lat]")
			}
			if !geo.Valid(geo.Point{Lat: c[1], Lng: c[0]}) {
				return nil, fmt.Errorf("zones: boundary point [%v, %v] is not a valid [lng, lat]", c[0], c[1])
			}
			ring = append(ring, []float64{c[0], c[1]})
		}
		if len(ring) > 1 && ring[0][0] == ring[len(ring)-1][0] && ring[0][1] == ring[len(ring)-1][1] {
			ring = ring[:len(ring)-1]
		}
		if len(ring) < 3 {
			return nil, fmt.Errorf("zones: a polygon needs at least 3 distinct points")
		}
		ring = append(ring, []float64{ring[0][0], ring[0][1]})
		if s.Center == nil {
			b, _ := geo.RingBBox(ring)
			c := b.Center()
			s.Center = &geo.Point{Lat: math.Round(c.Lat*1e6) / 1e6, Lng: math.Round(c.Lng*1e6) / 1e6}
		}
		return ring, nil
	default:
		return nil, fmt.Errorf("zones: shape must be circle or polygon")
	}
}

// compiledZone is a zone prepared for fast lookups: decoded settings plus bbox and area.
type compiledZone struct {
	ID        uuid.UUID
	Name      string
	Type      string
	Status    string
	Color     string
	Ring      [][]float64
	BBox      geo.BBox
	AreaKm2   float64
	Settings  ZoneSettings
	UpdatedAt time.Time
}

func compile(z *ent.GeoFence) (compiledZone, bool) {
	b, ok := geo.RingBBox(z.Boundary)
	if !ok || len(z.Boundary) < 3 {
		return compiledZone{}, false
	}
	return compiledZone{
		ID: z.ID, Name: z.Name, Type: z.ZoneType, Status: z.Status, Color: z.Color,
		Ring: z.Boundary, BBox: b, AreaKm2: geo.RingAreaKm2(z.Boundary),
		Settings: SettingsFromMetadata(z.Metadata), UpdatedAt: z.UpdatedAt,
	}, true
}

func (z compiledZone) contains(p geo.Point) bool {
	return z.BBox.Contains(p) && geo.InRing(p, z.Ring)
}

func (z compiledZone) servesOutlet(outletID uuid.UUID) bool {
	if len(z.Settings.OutletIDs) == 0 || outletID == uuid.Nil {
		return true
	}
	for _, id := range z.Settings.OutletIDs {
		if id == outletID {
			return true
		}
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case jsonNumber:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// jsonNumber matches encoding/json.Number without importing it into the hot path.
type jsonNumber interface{ Float64() (float64, error) }
