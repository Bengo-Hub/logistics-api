package zones

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/geofence"
	"github.com/bengobox/logistics-service/internal/ent/outlet"
	"github.com/bengobox/logistics-service/internal/ent/serviceconfig"
	"github.com/bengobox/logistics-service/internal/modules/zones/geo"
)

// Errors the HTTP layer maps to status codes.
var (
	ErrNotFound  = errors.New("zones: not found")
	ErrDuplicate = errors.New("zones: a zone with this name already exists")
)

// ZoneInput creates a zone or fully replaces an existing one.
type ZoneInput struct {
	Name     string         `json:"name"`
	ZoneType string         `json:"zone_type"`
	Status   string         `json:"status"`
	Color    string         `json:"color"`
	Boundary [][]float64    `json:"boundary,omitempty"`
	Settings ZoneSettings   `json:"settings"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ZonePatch updates selected fields of a zone. Settings, when present, replace the
// stored settings as a whole so the editor always round-trips one consistent contract.
type ZonePatch struct {
	Name     *string        `json:"name,omitempty"`
	ZoneType *string        `json:"zone_type,omitempty"`
	Status   *string        `json:"status,omitempty"`
	Color    *string        `json:"color,omitempty"`
	Boundary *[][]float64   `json:"boundary,omitempty"`
	Settings *ZoneSettings  `json:"settings,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// ZoneView is the API representation of a zone.
type ZoneView struct {
	ID        uuid.UUID      `json:"id"`
	TenantID  uuid.UUID      `json:"tenant_id"`
	Name      string         `json:"name"`
	ZoneType  string         `json:"zone_type"`
	Status    string         `json:"status"`
	Color     string         `json:"color"`
	Boundary  [][]float64    `json:"boundary"`
	Settings  ZoneSettings   `json:"settings"`
	AreaKm2   float64        `json:"area_km2"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// DistanceProvider is the routing dependency used for road distances. app.go adapts
// the routing service to it so this package stays free of routing internals.
type DistanceProvider interface {
	RoadKm(ctx context.Context, from, to geo.Point) (float64, error)
}

// Service owns service areas, the delivery quote policy and quoting for all tenants.
type Service struct {
	client   *ent.Client
	cache    *sharedcache.Aside
	distance DistanceProvider
	log      *zap.Logger
}

// NewService creates a new zone service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{client: client, log: log.Named("zones.service")}
}

// SetCache injects the cache helper (optional; caching is skipped if nil).
func (s *Service) SetCache(c *sharedcache.Aside) { s.cache = c }

// SetDistanceProvider injects road routing (optional; straight-line estimates otherwise).
func (s *Service) SetDistanceProvider(d DistanceProvider) { s.distance = d }

func listKey(tenantID uuid.UUID) string { return sharedcache.Key("log", "zones", tenantID.String()) }
func snapKey(tenantID uuid.UUID) string {
	return sharedcache.Key("log", "zones", "snap", tenantID.String())
}

// Invalidate drops every cached view of a tenant's zones, outlets and policy. Called on
// zone and policy writes, and by the outlet subscriber when an outlet moves.
func (s *Service) Invalidate(ctx context.Context, tenantID uuid.UUID) {
	s.cache.Invalidate(ctx, listKey(tenantID), snapKey(tenantID))
}

// ── CRUD ─────────────────────────────────────────────────────────────────────

// CreateZone validates and stores a new zone.
func (s *Service) CreateZone(ctx context.Context, tenantID uuid.UUID, in ZoneInput) (*ZoneView, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, fmt.Errorf("zones: name is required")
	}
	zoneType, status, color := defaultStr(in.ZoneType, TypeDelivery), defaultStr(in.Status, StatusActive), defaultStr(in.Color, "#3b82f6")
	if !validStatuses[status] {
		return nil, fmt.Errorf("zones: status must be active, inactive or draft")
	}
	settings := in.Settings
	boundary, err := normalize(zoneType, &settings, in.Boundary)
	if err != nil {
		return nil, err
	}
	z, err := s.client.GeoFence.Create().
		SetTenantID(tenantID).
		SetName(name).
		SetZoneType(zoneType).
		SetStatus(status).
		SetColor(color).
		SetBoundary(boundary).
		SetMetadata(settings.MergeIntoMetadata(in.Metadata)).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrDuplicate
		}
		return nil, fmt.Errorf("zones: create: %w", err)
	}
	s.Invalidate(ctx, tenantID)
	s.log.Info("zone created", zap.String("tenant_id", tenantID.String()), zap.String("zone", z.Name))
	return toView(z), nil
}

// GetZone returns one zone.
func (s *Service) GetZone(ctx context.Context, tenantID, zoneID uuid.UUID) (*ZoneView, error) {
	z, err := s.get(ctx, tenantID, zoneID)
	if err != nil {
		return nil, err
	}
	return toView(z), nil
}

// ListZones returns all zones for a tenant ordered by name (cached, invalidated on write).
func (s *Service) ListZones(ctx context.Context, tenantID uuid.UUID) ([]ZoneView, error) {
	return sharedcache.GetOrSet(ctx, s.cache, listKey(tenantID), sharedcache.TTLReference, func(ctx context.Context) ([]ZoneView, error) {
		rows, err := s.client.GeoFence.Query().
			Where(geofence.TenantID(tenantID)).
			Order(ent.Asc(geofence.FieldName)).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("zones: list: %w", err)
		}
		out := make([]ZoneView, 0, len(rows))
		for _, z := range rows {
			out = append(out, *toView(z))
		}
		return out, nil
	})
}

// UpdateZone applies a patch. Shape, centre, radius and boundary are re-validated
// together so a zone can never be stored half-edited.
func (s *Service) UpdateZone(ctx context.Context, tenantID, zoneID uuid.UUID, p ZonePatch) (*ZoneView, error) {
	z, err := s.get(ctx, tenantID, zoneID)
	if err != nil {
		return nil, err
	}
	zoneType := z.ZoneType
	if p.ZoneType != nil {
		zoneType = *p.ZoneType
	}
	settings := SettingsFromMetadata(z.Metadata)
	if p.Settings != nil {
		settings = *p.Settings
	}
	boundary := z.Boundary
	if p.Boundary != nil {
		boundary = *p.Boundary
	}
	if p.Settings != nil && p.Settings.Shape == ShapePolygon && p.Boundary != nil {
		settings.Center = nil // derive a fresh label point from the new polygon
	}
	boundary, err = normalize(zoneType, &settings, boundary)
	if err != nil {
		return nil, err
	}
	md := z.Metadata
	if p.Metadata != nil {
		md = p.Metadata
	}

	u := s.client.GeoFence.UpdateOne(z).
		SetZoneType(zoneType).
		SetBoundary(boundary).
		SetMetadata(settings.MergeIntoMetadata(md))
	if p.Name != nil {
		name := strings.TrimSpace(*p.Name)
		if name == "" {
			return nil, fmt.Errorf("zones: name is required")
		}
		u.SetName(name)
	}
	if p.Status != nil {
		if !validStatuses[*p.Status] {
			return nil, fmt.Errorf("zones: status must be active, inactive or draft")
		}
		u.SetStatus(*p.Status)
	}
	if p.Color != nil && *p.Color != "" {
		u.SetColor(*p.Color)
	}
	updated, err := u.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrDuplicate
		}
		return nil, fmt.Errorf("zones: update: %w", err)
	}
	s.Invalidate(ctx, tenantID)
	return toView(updated), nil
}

// DeleteZone removes a zone.
func (s *Service) DeleteZone(ctx context.Context, tenantID, zoneID uuid.UUID) error {
	n, err := s.client.GeoFence.Delete().
		Where(geofence.ID(zoneID), geofence.TenantID(tenantID)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("zones: delete: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	s.Invalidate(ctx, tenantID)
	return nil
}

func (s *Service) get(ctx context.Context, tenantID, zoneID uuid.UUID) (*ent.GeoFence, error) {
	z, err := s.client.GeoFence.Query().Where(geofence.ID(zoneID), geofence.TenantID(tenantID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("zones: get: %w", err)
	}
	return z, nil
}

func toView(z *ent.GeoFence) *ZoneView {
	return &ZoneView{
		ID: z.ID, TenantID: z.TenantID, Name: z.Name, ZoneType: z.ZoneType, Status: z.Status,
		Color: z.Color, Boundary: z.Boundary, Settings: SettingsFromMetadata(z.Metadata),
		AreaKm2: round2(geo.RingAreaKm2(z.Boundary)), Metadata: z.Metadata,
		CreatedAt: z.CreatedAt, UpdatedAt: z.UpdatedAt,
	}
}

// ── Policy ───────────────────────────────────────────────────────────────────

// PolicyView is a tenant's effective policy and where it came from.
type PolicyView struct {
	Policy    Policy     `json:"policy"`
	Source    string     `json:"source"` // tenant | platform | default
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// GetPolicy returns the tenant policy, else the platform default row, else built-in defaults.
func (s *Service) GetPolicy(ctx context.Context, tenantID uuid.UUID) (PolicyView, error) {
	rows, err := s.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(PolicyConfigKey),
			serviceconfig.Or(serviceconfig.TenantID(tenantID), serviceconfig.TenantIDIsNil())).
		All(ctx)
	if err != nil {
		return PolicyView{}, fmt.Errorf("zones: load policy: %w", err)
	}
	var tenantRow, platformRow *ent.ServiceConfig
	for _, r := range rows {
		if r.TenantID != nil && *r.TenantID == tenantID {
			tenantRow = r
		} else if r.TenantID == nil {
			platformRow = r
		}
	}
	for _, c := range []struct {
		row    *ent.ServiceConfig
		source string
	}{{tenantRow, "tenant"}, {platformRow, "platform"}} {
		if c.row == nil {
			continue
		}
		p, perr := ParsePolicy(c.row.ConfigValue)
		if perr != nil {
			s.log.Warn("invalid delivery policy, using defaults", zap.String("source", c.source), zap.Error(perr))
			continue
		}
		t := c.row.UpdatedAt
		return PolicyView{Policy: p, Source: c.source, UpdatedAt: &t}, nil
	}
	return PolicyView{Policy: DefaultPolicy(), Source: "default"}, nil
}

// SavePolicy validates and upserts the tenant's policy.
func (s *Service) SavePolicy(ctx context.Context, tenantID uuid.UUID, p Policy) (PolicyView, error) {
	if err := s.upsertPolicy(ctx, &tenantID, p); err != nil {
		return PolicyView{}, err
	}
	s.Invalidate(ctx, tenantID)
	return s.GetPolicy(ctx, tenantID)
}

// ResetPolicy drops the tenant's own policy so it follows the platform default again.
func (s *Service) ResetPolicy(ctx context.Context, tenantID uuid.UUID) (PolicyView, error) {
	if _, err := s.client.ServiceConfig.Delete().
		Where(serviceconfig.ConfigKey(PolicyConfigKey), serviceconfig.TenantID(tenantID)).
		Exec(ctx); err != nil {
		return PolicyView{}, fmt.Errorf("zones: reset policy: %w", err)
	}
	s.Invalidate(ctx, tenantID)
	return s.GetPolicy(ctx, tenantID)
}

// GetPlatformPolicy returns the platform default policy (the nil-tenant row), else the
// built-in defaults.
func (s *Service) GetPlatformPolicy(ctx context.Context) (PolicyView, error) {
	row, err := s.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(PolicyConfigKey), serviceconfig.TenantIDIsNil()).
		Only(ctx)
	if ent.IsNotFound(err) {
		return PolicyView{Policy: DefaultPolicy(), Source: "default"}, nil
	}
	if err != nil {
		return PolicyView{}, fmt.Errorf("zones: load platform policy: %w", err)
	}
	p, perr := ParsePolicy(row.ConfigValue)
	if perr != nil {
		return PolicyView{Policy: DefaultPolicy(), Source: "default"}, nil
	}
	t := row.UpdatedAt
	return PolicyView{Policy: p, Source: "platform", UpdatedAt: &t}, nil
}

// SavePlatformPolicy stores the platform default that every tenant without its own policy
// uses, then drops cached snapshots so those tenants quote with it straight away.
func (s *Service) SavePlatformPolicy(ctx context.Context, p Policy) (PolicyView, error) {
	if err := s.upsertPolicy(ctx, nil, p); err != nil {
		return PolicyView{}, err
	}
	// Only tenants with delivery areas have quote snapshots worth dropping.
	var tenantIDs []uuid.UUID
	if err := s.client.GeoFence.Query().GroupBy(geofence.FieldTenantID).Scan(ctx, &tenantIDs); err == nil {
		for _, id := range tenantIDs {
			s.Invalidate(ctx, id)
		}
	}
	return s.GetPlatformPolicy(ctx)
}

// upsertPolicy validates and writes one policy row; tenantID nil is the platform default.
func (s *Service) upsertPolicy(ctx context.Context, tenantID *uuid.UUID, p Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	q := s.client.ServiceConfig.Query().Where(serviceconfig.ConfigKey(PolicyConfigKey))
	if tenantID == nil {
		q = q.Where(serviceconfig.TenantIDIsNil())
	} else {
		q = q.Where(serviceconfig.TenantID(*tenantID))
	}
	existing, err := q.Only(ctx)
	switch {
	case err == nil:
		_, err = existing.Update().SetConfigValue(string(raw)).Save(ctx)
	case ent.IsNotFound(err):
		_, err = s.client.ServiceConfig.Create().
			SetNillableTenantID(tenantID).
			SetConfigKey(PolicyConfigKey).
			SetConfigValue(string(raw)).
			SetConfigType("json").
			SetDescription("Customer delivery quote policy: per-km fallback, geofence buffer and rounding").
			Save(ctx)
	}
	if err != nil {
		return fmt.Errorf("zones: save policy: %w", err)
	}
	return nil
}

// ── Quote and coverage ───────────────────────────────────────────────────────

// Quote prices a delivery to a point. It is the only delivery pricing entry point on
// the platform.
func (s *Service) Quote(ctx context.Context, tenantID uuid.UUID, in QuoteInput) (Quote, error) {
	snap, err := s.snapshot(ctx, tenantID)
	if err != nil {
		return Quote{}, err
	}
	return computeQuote(in, snap, s.distanceFunc(ctx)), nil
}

// QuoteZoneOnly resolves which active delivery zone contains a point without routing
// calls. Task creation uses it to tag drop-offs.
func (s *Service) QuoteZoneOnly(ctx context.Context, tenantID uuid.UUID, in QuoteInput) (Quote, error) {
	snap, err := s.snapshot(ctx, tenantID)
	if err != nil {
		return Quote{}, err
	}
	return computeQuote(in, snap, nil), nil
}

// CoverageZone is the light zone shape customer maps draw.
type CoverageZone struct {
	ID       uuid.UUID   `json:"id"`
	Name     string      `json:"name"`
	Type     string      `json:"zone_type"`
	Color    string      `json:"color"`
	Fee      float64     `json:"fee"`
	Free     bool        `json:"free"`
	Center   *geo.Point  `json:"center,omitempty"`
	Aliases  []string    `json:"aliases,omitempty"`
	Boundary [][]float64 `json:"boundary"`
}

// Coverage is the public summary of where a tenant delivers.
type Coverage struct {
	Zones    []CoverageZone `json:"zones"`
	Outlets  []OutletPoint  `json:"outlets"`
	Bounds   *[4]float64    `json:"bounds,omitempty"` // [minLng, minLat, maxLng, maxLat]
	Center   *geo.Point     `json:"center,omitempty"`
	MinFee   float64        `json:"min_fee"`
	HasFree  bool           `json:"has_free_zone"`
	Currency string         `json:"currency"`
	Policy   struct {
		Fallback  string  `json:"fallback"`
		BufferKm  float64 `json:"buffer_km"`
		PerKmRate float64 `json:"per_km_rate"`
	} `json:"policy"`
	Version string `json:"policy_version"`
}

// Coverage returns active zones, outlets and map bounds for customer-facing maps.
func (s *Service) Coverage(ctx context.Context, tenantID uuid.UUID, outletID uuid.UUID) (Coverage, error) {
	snap, err := s.snapshot(ctx, tenantID)
	if err != nil {
		return Coverage{}, err
	}
	c := Coverage{Outlets: snap.Outlets, Currency: snap.Policy.Currency, Version: snap.Version, Zones: []CoverageZone{}}
	c.Policy.Fallback, c.Policy.BufferKm, c.Policy.PerKmRate = snap.Policy.Fallback, snap.Policy.BufferKm, snap.Policy.PerKmRate
	var bounds *geo.BBox
	minFee := -1.0
	for _, z := range snap.Zones {
		if z.Status != StatusActive || (z.Type != TypeDelivery && z.Type != TypeExclusion) || !z.servesOutlet(outletID) {
			continue
		}
		c.Zones = append(c.Zones, CoverageZone{
			ID: z.ID, Name: z.Name, Type: z.Type, Color: z.Color, Fee: z.Settings.Fee, Free: z.Settings.Free,
			Center: z.Settings.Center, Aliases: z.Settings.Aliases, Boundary: z.Ring,
		})
		if z.Type != TypeDelivery {
			continue
		}
		if bounds == nil {
			b := z.BBox
			bounds = &b
		} else {
			b := bounds.Extend(z.BBox)
			bounds = &b
		}
		fee := z.Settings.Fee
		if z.Settings.Free {
			fee, c.HasFree = 0, true
		}
		if minFee < 0 || fee < minFee {
			minFee = fee
		}
	}
	if minFee >= 0 {
		c.MinFee = minFee
	}
	if bounds != nil {
		c.Bounds = &[4]float64{bounds.MinLng, bounds.MinLat, bounds.MaxLng, bounds.MaxLat}
		ctr := bounds.Center()
		c.Center = &ctr
	}
	for _, o := range snap.Outlets {
		if outletID == uuid.Nil || o.ID == outletID {
			p := o.Point
			c.Center = &p
			break
		}
	}
	return c, nil
}

// NearestArea returns the zone containing a point, else the nearest active delivery
// zone, with the distance to its edge. The geocode proxy uses it to label places.
func (s *Service) NearestArea(ctx context.Context, tenantID uuid.UUID, p geo.Point) (*ZoneRef, float64, error) {
	snap, err := s.snapshot(ctx, tenantID)
	if err != nil {
		return nil, 0, err
	}
	var best *compiledZone
	bestKm := -1.0
	for i := range snap.Zones {
		z := &snap.Zones[i]
		if z.Status != StatusActive || z.Type != TypeDelivery {
			continue
		}
		d := geo.DistanceToRingKm(p, z.Ring)
		// Prefer the most specific zone among those containing the point.
		if best == nil || d < bestKm || (d == 0 && bestKm == 0 && z.AreaKm2 < best.AreaKm2) {
			best, bestKm = z, d
		}
	}
	if best == nil {
		return nil, 0, nil
	}
	return &ZoneRef{ID: best.ID, Name: best.Name}, round2(bestKm), nil
}

// snapshot loads (or returns the cached) zones, outlets and policy for one tenant.
func (s *Service) snapshot(ctx context.Context, tenantID uuid.UUID) (snapshot, error) {
	return sharedcache.GetOrSet(ctx, s.cache, snapKey(tenantID), sharedcache.TTLReference, func(ctx context.Context) (snapshot, error) {
		rows, err := s.client.GeoFence.Query().
			Where(geofence.TenantID(tenantID), geofence.Status(StatusActive)).
			All(ctx)
		if err != nil {
			return snapshot{}, fmt.Errorf("zones: load: %w", err)
		}
		zs := make([]compiledZone, 0, len(rows))
		for _, r := range rows {
			if cz, ok := compile(r); ok {
				zs = append(zs, cz)
			}
		}
		outs, err := s.client.Outlet.Query().
			Where(outlet.TenantID(tenantID), outlet.Status("active"), outlet.LatitudeNotNil(), outlet.LongitudeNotNil()).
			All(ctx)
		if err != nil {
			return snapshot{}, fmt.Errorf("zones: load outlets: %w", err)
		}
		points := make([]OutletPoint, 0, len(outs))
		for _, o := range outs {
			p := geo.Point{Lat: *o.Latitude, Lng: *o.Longitude}
			if geo.Valid(p) {
				points = append(points, OutletPoint{ID: o.ID, Name: o.Name, Point: p})
			}
		}
		pv, err := s.GetPolicy(ctx, tenantID)
		if err != nil {
			return snapshot{}, err
		}
		var pUpdated time.Time
		if pv.UpdatedAt != nil {
			pUpdated = *pv.UpdatedAt
		}
		return snapshot{Zones: zs, Outlets: points, Policy: pv.Policy, Version: versionOf(zs, pUpdated)}, nil
	})
}

func (s *Service) distanceFunc(ctx context.Context) DistanceFunc {
	if s.distance == nil {
		return nil
	}
	return func(from, to geo.Point) (float64, bool) {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		km, err := s.distance.RoadKm(cctx, from, to)
		if err != nil {
			s.log.Debug("road distance unavailable, using estimate", zap.Error(err))
			return 0, false
		}
		return km, true
	}
}

func defaultStr(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return strings.TrimSpace(v)
}

// DistanceFee prices a distance with the tenant's per-km policy (base, rate, minimum,
// rounding). Rider earnings use it when no rider pricing rule exists.
func (s *Service) DistanceFee(ctx context.Context, tenantID uuid.UUID, km float64) (float64, error) {
	pv, err := s.GetPolicy(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	return perKmFee(pv.Policy, km), nil
}

// DropoffTag is what task creation records about a delivery destination.
type DropoffTag struct {
	Zone       *ZoneRef
	DistanceKm float64
}

// TagDropoff resolves the delivery area for a drop-off and the trip distance. The pickup
// point is used for distance when known; otherwise the dispatching outlet is used.
func (s *Service) TagDropoff(ctx context.Context, tenantID, outletID uuid.UUID, pickup, dropoff geo.Point) (DropoffTag, error) {
	q, err := s.QuoteZoneOnly(ctx, tenantID, QuoteInput{Point: dropoff, OutletID: outletID})
	if err != nil {
		return DropoffTag{}, err
	}
	tag := DropoffTag{}
	if q.Serviceable && q.Method == MethodZone {
		tag.Zone = q.Zone
	}
	from := pickup
	if !geo.Valid(from) && q.Outlet != nil {
		from = q.Outlet.Point
	}
	if geo.Valid(from) && geo.Valid(dropoff) {
		snap, serr := s.snapshot(ctx, tenantID)
		pol := DefaultPolicy()
		if serr == nil {
			pol = snap.Policy
		}
		tag.DistanceKm, _ = measure(from, dropoff, pol, s.distanceFunc(ctx))
	}
	return tag, nil
}

// TagTaskDropoff adapts TagDropoff to the tasks module's interface.
func (s *Service) TagTaskDropoff(ctx context.Context, tenantID, outletID uuid.UUID, pickupLat, pickupLng, dropLat, dropLng float64) (string, string, float64, error) {
	tag, err := s.TagDropoff(ctx, tenantID, outletID, geo.Point{Lat: pickupLat, Lng: pickupLng}, geo.Point{Lat: dropLat, Lng: dropLng})
	if err != nil {
		return "", "", 0, err
	}
	if tag.Zone == nil {
		return "", "", tag.DistanceKm, nil
	}
	return tag.Zone.ID.String(), tag.Zone.Name, tag.DistanceKm, nil
}
