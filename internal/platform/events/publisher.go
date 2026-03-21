package events

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"
)

// Publisher handles publishing events to NATS.
type Publisher struct {
	conn   *nats.Conn
	logger *zap.Logger
}

// NewPublisher creates a new event publisher.
func NewPublisher(conn *nats.Conn, logger *zap.Logger) *Publisher {
	return &Publisher{
		conn:   conn,
		logger: logger.Named("events.publisher"),
	}
}

// Event represents a CloudEvents-compatible event envelope.
type Event struct {
	ID              string                 `json:"id"`
	Source          string                 `json:"source"`
	SpecVersion     string                 `json:"specversion"`
	Type            string                 `json:"type"`
	DataContentType string                 `json:"datacontenttype"`
	Time            string                 `json:"time"`
	TenantID        string                 `json:"tenantId,omitempty"`
	TenantSlug      string                 `json:"tenant_slug,omitempty"`
	Data            map[string]interface{} `json:"data"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

// NewEvent creates a new event with the given type and data.
func NewEvent(eventType string, tenantID uuid.UUID, data map[string]interface{}) Event {
	return Event{
		ID:              uuid.New().String(),
		Source:          "logistics-service",
		SpecVersion:     "1.0",
		Type:            eventType,
		DataContentType: "application/json",
		Time:            time.Now().UTC().Format(time.RFC3339),
		TenantID:        tenantID.String(),
		Data:            data,
		Metadata: map[string]interface{}{
			"correlation_id": uuid.New().String(),
			"source":         "logistics-service",
		},
	}
}

// Publish publishes an event to the specified subject.
func (p *Publisher) Publish(ctx context.Context, subject string, event Event) error {
	if p == nil || p.conn == nil {
		return nil
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	if err := p.conn.Publish(subject, data); err != nil {
		p.logger.Error("failed to publish event",
			zap.Error(err),
			zap.String("subject", subject),
			zap.String("event_id", event.ID))
		return fmt.Errorf("publish event: %w", err)
	}

	p.logger.Debug("event published",
		zap.String("subject", subject),
		zap.String("event_type", event.Type),
		zap.String("event_id", event.ID))

	return nil
}

// --- Fleet Member Events ---

// FleetMemberEventData represents data for fleet member lifecycle events.
type FleetMemberEventData struct {
	MemberID string `json:"member_id"`
	UserID   string `json:"user_id"`
	FleetID  string `json:"fleet_id"`
	UserEmail string `json:"user_email"`
	UserName  string `json:"user_name"`
}

func (d FleetMemberEventData) toMap() map[string]interface{} {
	return map[string]interface{}{
		"member_id":  d.MemberID,
		"user_id":    d.UserID,
		"fleet_id":   d.FleetID,
		"user_email": d.UserEmail,
		"user_name":  d.UserName,
	}
}

// PublishFleetMemberInvited publishes a fleet.member_invited event.
func (p *Publisher) PublishFleetMemberInvited(ctx context.Context, tenantID uuid.UUID, data FleetMemberEventData) error {
	event := NewEvent("logistics.fleet.member_invited", tenantID, data.toMap())
	return p.Publish(ctx, "logistics.fleet.member_invited", event)
}

// PublishFleetMemberApproved publishes a fleet.member_approved event.
func (p *Publisher) PublishFleetMemberApproved(ctx context.Context, tenantID uuid.UUID, data FleetMemberEventData) error {
	event := NewEvent("logistics.fleet.member_approved", tenantID, data.toMap())
	return p.Publish(ctx, "logistics.fleet.member_approved", event)
}

// PublishFleetMemberSuspended publishes a fleet.member_suspended event.
func (p *Publisher) PublishFleetMemberSuspended(ctx context.Context, tenantID uuid.UUID, data FleetMemberEventData) error {
	event := NewEvent("logistics.fleet.member_suspended", tenantID, data.toMap())
	return p.Publish(ctx, "logistics.fleet.member_suspended", event)
}
