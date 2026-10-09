package zones

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/zones/geo"
)

// Place is one geocoding result, labelled with the tenant's delivery area when known.
type Place struct {
	Name        string    `json:"name"`
	DisplayName string    `json:"display_name"`
	Location    geo.Point `json:"location"`
	Kind        string    `json:"kind,omitempty"`
	Source      string    `json:"source"` // zone | geocoder
	Area        *ZoneRef  `json:"area,omitempty"`
	AreaKm      float64   `json:"area_km,omitempty"`
}

// Geocoder proxies a Nominatim-compatible geocoder for every frontend so browsers never
// call third-party geocoders directly. Results are cached in Redis and outbound calls are
// throttled to one per second across all pods, as the public Nominatim policy requires.
type Geocoder struct {
	baseURL   string
	userAgent string
	http      *http.Client
	zones     *Service
	cache     *sharedcache.Aside
	log       *zap.Logger
}

// NewGeocoder builds the proxy. cache may be nil (no caching, no cross-pod throttle).
func NewGeocoder(baseURL, userAgent string, zones *Service, cache *sharedcache.Aside, log *zap.Logger) *Geocoder {
	return &Geocoder{
		baseURL:   strings.TrimRight(baseURL, "/"),
		userAgent: userAgent,
		http:      &http.Client{Timeout: 6 * time.Second},
		zones:     zones,
		cache:     cache,
		log:       log.Named("zones.geocoder"),
	}
}

type nominatimHit struct {
	Lat         string `json:"lat"`
	Lon         string `json:"lon"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Type        string `json:"type"`
	Address     struct {
		Road, Suburb, Village, Town, City, Hamlet, Neighbourhood, County string
	} `json:"address"`
}

// Search finds places matching q. The tenant's own areas (name or alias match) come
// first, then geocoder results biased to the tenant's coverage bounds.
func (g *Geocoder) Search(ctx context.Context, tenantID uuid.UUID, q string, limit int) ([]Place, error) {
	q = strings.TrimSpace(q)
	if len([]rune(q)) < 2 {
		return []Place{}, nil
	}
	if limit <= 0 || limit > 10 {
		limit = 8
	}
	cov, err := g.zones.Coverage(ctx, tenantID, uuid.Nil)
	if err != nil {
		return nil, err
	}

	out := make([]Place, 0, limit)
	lq := strings.ToLower(q)
	for _, z := range cov.Zones {
		if z.Type != TypeDelivery || z.Center == nil {
			continue
		}
		match := strings.Contains(strings.ToLower(z.Name), lq)
		for _, a := range z.Aliases {
			match = match || strings.Contains(strings.ToLower(a), lq)
		}
		if match {
			ref := ZoneRef{ID: z.ID, Name: z.Name}
			out = append(out, Place{Name: z.Name, DisplayName: z.Name + " (delivery area)", Location: *z.Center, Source: "zone", Area: &ref})
		}
	}

	params := url.Values{"q": {q}, "format": {"jsonv2"}, "addressdetails": {"1"}, "limit": {strconv.Itoa(limit)}}
	if cov.Bounds != nil {
		b := cov.Bounds
		// Pad the coverage box so places just outside the areas still show up.
		params.Set("viewbox", fmt.Sprintf("%.5f,%.5f,%.5f,%.5f", b[0]-0.15, b[3]+0.15, b[2]+0.15, b[1]-0.15))
	}
	key := sharedcache.Key("log", "geocode", "s", tenantID.String(), strings.ToLower(q))
	hits, err := sharedcache.GetOrSet(ctx, g.cache, key, time.Hour, func(ctx context.Context) ([]nominatimHit, error) {
		var res []nominatimHit
		return res, g.get(ctx, "/search", params, &res)
	})
	if err != nil {
		g.log.Warn("geocoder search failed", zap.Error(err))
		return out, nil // areas still help even when the geocoder is down
	}
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		p, ok := h.place()
		if !ok {
			continue
		}
		g.label(ctx, tenantID, &p)
		out = append(out, p)
	}
	return out, nil
}

// Reverse names the place at a point, e.g. "Alupe Market" plus the delivery area.
func (g *Geocoder) Reverse(ctx context.Context, tenantID uuid.UUID, p geo.Point) (Place, error) {
	if !geo.Valid(p) {
		return Place{}, fmt.Errorf("zones: invalid location")
	}
	params := url.Values{
		"lat": {strconv.FormatFloat(p.Lat, 'f', 6, 64)}, "lon": {strconv.FormatFloat(p.Lng, 'f', 6, 64)},
		"format": {"jsonv2"}, "addressdetails": {"1"}, "zoom": {"17"},
	}
	// About 11 m of rounding keeps the cache useful while a pin is dragged.
	key := sharedcache.Key("log", "geocode", "r", fmt.Sprintf("%.4f,%.4f", p.Lat, p.Lng))
	hit, err := sharedcache.GetOrSet(ctx, g.cache, key, 24*time.Hour, func(ctx context.Context) (nominatimHit, error) {
		var res nominatimHit
		return res, g.get(ctx, "/reverse", params, &res)
	})
	place := Place{Location: p, Source: "geocoder"}
	if err == nil {
		if named, ok := hit.place(); ok {
			place.Name, place.DisplayName, place.Kind = named.Name, named.DisplayName, named.Kind
		}
	} else {
		g.log.Warn("geocoder reverse failed", zap.Error(err))
	}
	place.Location = p // keep the exact pin, not the geocoder's snapped point
	g.label(ctx, tenantID, &place)
	if place.Name == "" {
		if place.Area != nil {
			place.Name = "Near " + place.Area.Name
		} else {
			place.Name = fmt.Sprintf("%.5f, %.5f", p.Lat, p.Lng)
		}
		place.DisplayName = place.Name
	}
	return place, nil
}

func (g *Geocoder) label(ctx context.Context, tenantID uuid.UUID, p *Place) {
	ref, km, err := g.zones.NearestArea(ctx, tenantID, p.Location)
	if err == nil && ref != nil {
		p.Area, p.AreaKm = ref, km
	}
}

func (h nominatimHit) place() (Place, bool) {
	lat, err1 := strconv.ParseFloat(h.Lat, 64)
	lng, err2 := strconv.ParseFloat(h.Lon, 64)
	if err1 != nil || err2 != nil {
		return Place{}, false
	}
	name := h.Name
	for _, c := range []string{h.Address.Road, h.Address.Neighbourhood, h.Address.Suburb, h.Address.Hamlet, h.Address.Village, h.Address.Town, h.Address.City} {
		if name != "" {
			break
		}
		name = c
	}
	if name == "" {
		name = strings.SplitN(h.DisplayName, ",", 2)[0]
	}
	return Place{Name: name, DisplayName: h.DisplayName, Location: geo.Point{Lat: lat, Lng: lng}, Kind: h.Type, Source: "geocoder"}, true
}

func (g *Geocoder) get(ctx context.Context, path string, params url.Values, dst any) error {
	if err := g.waitSlot(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+path+"?"+params.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", g.userAgent)
	req.Header.Set("Accept-Language", "en")
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("geocoder status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// waitSlot takes the shared one-request-per-second slot, waiting up to 3 seconds.
func (g *Geocoder) waitSlot(ctx context.Context) error {
	if g.cache == nil || g.cache.Client() == nil {
		return nil
	}
	rdb := g.cache.Client()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ok, err := rdb.SetNX(ctx, "log:geocode:slot", 1, time.Second).Result()
		if err != nil || ok {
			return nil // fail open on Redis errors; the cache still protects the geocoder
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("geocoder busy")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
