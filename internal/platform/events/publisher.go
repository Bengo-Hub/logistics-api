package events

import (
	"context"
	"database/sql"
	"fmt"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Publisher handles publishing logistics events via the transactional outbox pattern.
type Publisher struct {
	repo   eventslib.OutboxRepository
	logger *zap.Logger
}

// NewPublisher creates a new event publisher backed by the shared-events outbox.
func NewPublisher(sqlDB *sql.DB, logger *zap.Logger) *Publisher {
	return &Publisher{
		repo:   eventslib.NewSQLOutboxRepository(sqlDB),
		logger: logger.Named("events.publisher"),
	}
}

// OutboxRepo returns the outbox repository for use by the background publisher.
func (p *Publisher) OutboxRepo() eventslib.OutboxRepository {
	return p.repo
}

// publish writes an event to the outbox for background publishing to NATS.
func (p *Publisher) publish(ctx context.Context, tenantID uuid.UUID, aggregateType, eventType string, data map[string]interface{}) error {
	if p == nil {
		return nil
	}

	event := eventslib.NewEvent(eventType, aggregateType, uuid.New(), tenantID, data)

	tx, err := p.repo.BeginTx(ctx)
	if err != nil {
		p.logger.Error("failed to begin tx for event", zap.String("event_type", eventType), zap.Error(err))
		return fmt.Errorf("begin tx: %w", err)
	}

	if err := eventslib.CreateOutboxRecordInTx(ctx, tx, p.repo, event); err != nil {
		_ = tx.Rollback()
		p.logger.Error("failed to write event to outbox", zap.String("event_type", eventType), zap.Error(err))
		return fmt.Errorf("write outbox: %w", err)
	}

	if err := tx.Commit(); err != nil {
		p.logger.Error("failed to commit event", zap.String("event_type", eventType), zap.Error(err))
		return fmt.Errorf("commit: %w", err)
	}

	p.logger.Debug("event written to outbox",
		zap.String("event_type", eventType),
		zap.String("subject", event.Subject()))

	return nil
}

// --- Fleet Member Events ---

// FleetMemberEventData represents data for fleet member lifecycle events.
type FleetMemberEventData struct {
	MemberID  string `json:"member_id"`
	UserID    string `json:"user_id"`
	FleetID   string `json:"fleet_id"`
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
	return p.publish(ctx, tenantID, "logistics", "fleet.member_invited", data.toMap())
}

// PublishFleetMemberApproved publishes a fleet.member_approved event.
func (p *Publisher) PublishFleetMemberApproved(ctx context.Context, tenantID uuid.UUID, data FleetMemberEventData) error {
	return p.publish(ctx, tenantID, "logistics", "fleet.member_approved", data.toMap())
}

// PublishFleetMemberSuspended publishes a fleet.member_suspended event.
func (p *Publisher) PublishFleetMemberSuspended(ctx context.Context, tenantID uuid.UUID, data FleetMemberEventData) error {
	return p.publish(ctx, tenantID, "logistics", "fleet.member_suspended", data.toMap())
}

// --- Task Lifecycle Events ---

// TaskEventData represents data for task lifecycle events.
type TaskEventData struct {
	TaskID            string `json:"task_id"`
	TrackingCode      string `json:"tracking_code"`
	ExternalReference string `json:"external_reference,omitempty"`
	Status            string `json:"status"`
	PreviousStatus    string `json:"previous_status,omitempty"`
	FleetMemberID     string `json:"fleet_member_id,omitempty"`
	SourceService     string `json:"source_service,omitempty"`
}

func (d TaskEventData) toMap() map[string]interface{} {
	m := map[string]interface{}{
		"task_id":       d.TaskID,
		"tracking_code": d.TrackingCode,
		"status":        d.Status,
	}
	if d.ExternalReference != "" {
		m["external_reference"] = d.ExternalReference
	}
	if d.PreviousStatus != "" {
		m["previous_status"] = d.PreviousStatus
	}
	if d.FleetMemberID != "" {
		m["fleet_member_id"] = d.FleetMemberID
	}
	if d.SourceService != "" {
		m["source_service"] = d.SourceService
	}
	return m
}

// PublishTaskAssigned publishes a logistics.task.assigned event.
func (p *Publisher) PublishTaskAssigned(ctx context.Context, tenantID uuid.UUID, data TaskEventData) error {
	return p.publish(ctx, tenantID, "logistics", "task.assigned", data.toMap())
}

// PublishTaskStatusChanged publishes a logistics.task.status_changed event.
func (p *Publisher) PublishTaskStatusChanged(ctx context.Context, tenantID uuid.UUID, data TaskEventData) error {
	eventType := fmt.Sprintf("task.%s", data.Status)
	return p.publish(ctx, tenantID, "logistics", eventType, data.toMap())
}

// PublishTaskCompleted publishes a logistics.task.completed event.
func (p *Publisher) PublishTaskCompleted(ctx context.Context, tenantID uuid.UUID, data TaskEventData) error {
	return p.publish(ctx, tenantID, "logistics", "task.completed", data.toMap())
}
