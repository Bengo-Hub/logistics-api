package telemetry

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/telemetrystream"
)

// LocationPoint is the ingest payload for a single GPS location update.
type LocationPoint struct {
	Latitude   float64   `json:"lat"`
	Longitude  float64   `json:"lng"`
	SpeedKph   *float64  `json:"speed_kph,omitempty"`
	BearingDeg *float64  `json:"bearing_deg,omitempty"`
	AccuracyM  *float64  `json:"accuracy_m,omitempty"`
	AltitudeM  *float64  `json:"altitude_m,omitempty"`
	BatteryPct *float64  `json:"battery_pct,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
	DeviceID   string    `json:"device_id,omitempty"`
}

// LocationPublisher is optional — updates Redis GEO for dispatch nearest-rider queries.
type LocationPublisher interface {
	UpdateRiderLocation(ctx context.Context, tenantID, memberID uuid.UUID, lat, lng float64) error
}

// Service handles telemetry ingestion and stream queries.
type Service struct {
	log       *zap.Logger
	client    *ent.Client
	locPub    LocationPublisher
}

// NewService creates a new telemetry Service.
func NewService(log *zap.Logger, client *ent.Client, locPub LocationPublisher) *Service {
	return &Service{
		log:    log.Named("telemetry.service"),
		client: client,
		locPub: locPub,
	}
}

// IngestLocation records a rider's location into the active telemetry stream.
// If no active stream exists, a new one is opened automatically.
// Also updates the Redis GEO index for dispatch queries.
func (s *Service) IngestLocation(ctx context.Context, tenantID, memberID uuid.UUID, pt LocationPoint) error {
	if pt.CapturedAt.IsZero() {
		pt.CapturedAt = time.Now().UTC()
	}

	stream, err := s.activeStream(ctx, tenantID, memberID, pt.DeviceID)
	if err != nil {
		return fmt.Errorf("telemetry: get active stream: %w", err)
	}

	metadata := map[string]any{
		"lat": pt.Latitude,
		"lng": pt.Longitude,
	}

	c := s.client.TelemetryPoint.Create().
		SetStreamID(stream.ID).
		SetCapturedAt(pt.CapturedAt).
		SetMetadata(metadata)

	if pt.SpeedKph != nil {
		c = c.SetSpeedKph(*pt.SpeedKph)
	}
	if pt.BearingDeg != nil {
		c = c.SetBearingDeg(*pt.BearingDeg)
	}
	if pt.AccuracyM != nil {
		c = c.SetAccuracyM(*pt.AccuracyM)
	}
	if pt.AltitudeM != nil {
		c = c.SetAltitudeM(*pt.AltitudeM)
	}
	if pt.BatteryPct != nil {
		c = c.SetBatteryPct(*pt.BatteryPct)
	}

	if _, err := c.Save(ctx); err != nil {
		return fmt.Errorf("telemetry: save point: %w", err)
	}

	// Update Redis GEO for real-time dispatch (non-fatal if unavailable)
	if s.locPub != nil {
		if err := s.locPub.UpdateRiderLocation(ctx, tenantID, memberID, pt.Latitude, pt.Longitude); err != nil {
			s.log.Warn("telemetry: redis geo update failed", zap.Error(err))
		}
	}

	return nil
}

// EndStream marks the active stream as ended.
func (s *Service) EndStream(ctx context.Context, tenantID, memberID uuid.UUID) error {
	stream, err := s.client.TelemetryStream.Query().
		Where(
			telemetrystream.TenantID(tenantID),
			telemetrystream.FleetMemberID(memberID),
			telemetrystream.Status("active"),
		).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil // no active stream — nothing to end
		}
		return fmt.Errorf("telemetry: find active stream: %w", err)
	}

	now := time.Now().UTC()
	if _, err := s.client.TelemetryStream.UpdateOne(stream).
		SetStatus("ended").
		SetEndedAt(now).
		Save(ctx); err != nil {
		return fmt.Errorf("telemetry: end stream: %w", err)
	}
	return nil
}

// activeStream returns the current active stream for the member, creating one if necessary.
func (s *Service) activeStream(ctx context.Context, tenantID, memberID uuid.UUID, deviceID string) (*ent.TelemetryStream, error) {
	stream, err := s.client.TelemetryStream.Query().
		Where(
			telemetrystream.TenantID(tenantID),
			telemetrystream.FleetMemberID(memberID),
			telemetrystream.Status("active"),
		).Only(ctx)
	if err == nil {
		return stream, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}

	// Create a new stream
	c := s.client.TelemetryStream.Create().
		SetTenantID(tenantID).
		SetFleetMemberID(memberID).
		SetStartedAt(time.Now().UTC()).
		SetStatus("active")
	if deviceID != "" {
		c = c.SetDeviceID(deviceID)
	}

	return c.Save(ctx)
}
