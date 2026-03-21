package zones

import (
	"context"
	"fmt"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/geofence"
)

// CreateZoneRequest is the DTO for creating a delivery zone.
type CreateZoneRequest struct {
	Name     string       `json:"name"`
	ZoneType string       `json:"zone_type"`
	Status   string       `json:"status"`
	Boundary [][]float64  `json:"boundary"`
	Color    string       `json:"color"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// UpdateZoneRequest is the DTO for updating a delivery zone.
type UpdateZoneRequest struct {
	Name     *string        `json:"name,omitempty"`
	ZoneType *string        `json:"zone_type,omitempty"`
	Status   *string        `json:"status,omitempty"`
	Boundary *[][]float64   `json:"boundary,omitempty"`
	Color    *string        `json:"color,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Service handles geo-fence/zone business logic.
type Service struct {
	client *ent.Client
	cache  *sharedcache.Aside
	log    *zap.Logger
}

// NewService creates a new zone service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("zones.service"),
	}
}

// SetCache injects the cache helper (optional; caching is skipped if nil).
func (s *Service) SetCache(c *sharedcache.Aside) {
	s.cache = c
}

// CreateZone creates a new delivery zone.
func (s *Service) CreateZone(ctx context.Context, tenantID uuid.UUID, req CreateZoneRequest) (*ent.GeoFence, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("zones: name is required")
	}
	if len(req.Boundary) < 3 {
		return nil, fmt.Errorf("zones: boundary must have at least 3 coordinate pairs")
	}

	zoneType := req.ZoneType
	if zoneType == "" {
		zoneType = "delivery"
	}
	status := req.Status
	if status == "" {
		status = "active"
	}
	color := req.Color
	if color == "" {
		color = "#3b82f6"
	}

	builder := s.client.GeoFence.Create().
		SetTenantID(tenantID).
		SetName(req.Name).
		SetZoneType(zoneType).
		SetStatus(status).
		SetBoundary(req.Boundary).
		SetColor(color)

	if req.Metadata != nil {
		builder.SetMetadata(req.Metadata)
	}

	zone, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("zones: create: %w", err)
	}

	s.log.Info("zone created",
		zap.String("zone_id", zone.ID.String()),
		zap.String("name", zone.Name),
	)
	return zone, nil
}

// GetZone returns a zone by ID.
func (s *Service) GetZone(ctx context.Context, tenantID, zoneID uuid.UUID) (*ent.GeoFence, error) {
	zone, err := s.client.GeoFence.Query().
		Where(geofence.ID(zoneID), geofence.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("zones: not found")
		}
		return nil, fmt.Errorf("zones: get: %w", err)
	}
	return zone, nil
}

// ListZones returns all zones for a tenant (cached 5 min).
func (s *Service) ListZones(ctx context.Context, tenantID uuid.UUID) ([]*ent.GeoFence, error) {
	key := sharedcache.Key("log", "zones", tenantID.String())
	fetch := func(ctx context.Context) ([]*ent.GeoFence, error) {
		zones, err := s.client.GeoFence.Query().
			Where(geofence.TenantID(tenantID)).
			Order(ent.Asc(geofence.FieldName)).
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("zones: list: %w", err)
		}
		return zones, nil
	}
	return sharedcache.GetOrSet(ctx, s.cache, key, sharedcache.TTLReference, fetch)
}

// UpdateZone updates an existing zone.
func (s *Service) UpdateZone(ctx context.Context, tenantID, zoneID uuid.UUID, req UpdateZoneRequest) (*ent.GeoFence, error) {
	zone, err := s.client.GeoFence.Query().
		Where(geofence.ID(zoneID), geofence.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("zones: not found")
		}
		return nil, fmt.Errorf("zones: query for update: %w", err)
	}

	builder := s.client.GeoFence.UpdateOne(zone)
	if req.Name != nil {
		builder.SetName(*req.Name)
	}
	if req.ZoneType != nil {
		builder.SetZoneType(*req.ZoneType)
	}
	if req.Status != nil {
		builder.SetStatus(*req.Status)
	}
	if req.Boundary != nil {
		if len(*req.Boundary) < 3 {
			return nil, fmt.Errorf("zones: boundary must have at least 3 coordinate pairs")
		}
		builder.SetBoundary(*req.Boundary)
	}
	if req.Color != nil {
		builder.SetColor(*req.Color)
	}
	if req.Metadata != nil {
		builder.SetMetadata(req.Metadata)
	}

	updated, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("zones: update: %w", err)
	}

	s.log.Info("zone updated",
		zap.String("zone_id", zoneID.String()),
	)
	return updated, nil
}

// DeleteZone deletes a zone.
func (s *Service) DeleteZone(ctx context.Context, tenantID, zoneID uuid.UUID) error {
	count, err := s.client.GeoFence.Delete().
		Where(geofence.ID(zoneID), geofence.TenantID(tenantID)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("zones: delete: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("zones: not found")
	}

	s.log.Info("zone deleted", zap.String("zone_id", zoneID.String()))
	return nil
}
