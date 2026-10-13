package notifications

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/logisticsnotification"
	"github.com/bengobox/logistics-service/internal/ent/predicate"
)

// Service creates and queries notifications for two audiences: the tenant's dispatchers
// (recipient nil, team-visible operational alerts) and a single rider (recipient set, the rider
// app inbox). Each new one is pushed live through the Hub to that audience only.
type Service struct {
	log    *zap.Logger
	client *ent.Client
	hub    *Hub
}

// NewService creates a new Service. hub may be nil (persistence still works; nothing gets
// pushed live).
func NewService(log *zap.Logger, client *ent.Client, hub *Hub) *Service {
	return &Service{
		log:    log.Named("notifications.service"),
		client: client,
		hub:    hub,
	}
}

// CreateRequest describes a new notification to record and broadcast.
type CreateRequest struct {
	TenantID         uuid.UUID
	NotificationType string
	Title            string
	Body             string
	Payload          map[string]any
	RelatedTaskID    *uuid.UUID
	// RecipientMemberID makes this a message for one rider; nil is a dispatcher alert.
	RecipientMemberID *uuid.UUID
}

// audience selects one audience's rows: a rider's inbox, or the dispatcher feed when member is nil.
func audience(tenantID uuid.UUID, member *uuid.UUID) predicate.LogisticsNotification {
	if member == nil {
		return logisticsnotification.And(
			logisticsnotification.TenantID(tenantID),
			logisticsnotification.RecipientMemberIDIsNil(),
		)
	}
	return logisticsnotification.And(
		logisticsnotification.TenantID(tenantID),
		logisticsnotification.RecipientMemberID(*member),
	)
}

// Create persists a new notification and pushes it to its audience.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*ent.LogisticsNotification, error) {
	c := s.client.LogisticsNotification.Create().
		SetTenantID(req.TenantID).
		SetNotificationType(req.NotificationType).
		SetTitle(req.Title).
		SetNillableRelatedTaskID(req.RelatedTaskID).
		SetNillableRecipientMemberID(req.RecipientMemberID)
	if req.Body != "" {
		c = c.SetBody(req.Body)
	}
	if req.Payload != nil {
		c = c.SetPayload(req.Payload)
	}

	n, err := c.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("notifications: create: %w", err)
	}

	if s.hub != nil {
		s.hub.BroadcastNew(n)
	}
	return n, nil
}

// CreateDeduped is Create, but skips creating a new row (and returns nil, nil) if the same
// audience already has an unread notification of the same type and related task. Used by
// triggers that can fire repeatedly for one condition (e.g. the SLA monitor's periodic scan).
func (s *Service) CreateDeduped(ctx context.Context, req CreateRequest) (*ent.LogisticsNotification, error) {
	if req.RelatedTaskID != nil {
		exists, err := s.client.LogisticsNotification.Query().
			Where(
				audience(req.TenantID, req.RecipientMemberID),
				logisticsnotification.NotificationType(req.NotificationType),
				logisticsnotification.RelatedTaskID(*req.RelatedTaskID),
				logisticsnotification.IsRead(false),
			).
			Exist(ctx)
		if err != nil {
			return nil, fmt.Errorf("notifications: dedup check: %w", err)
		}
		if exists {
			return nil, nil
		}
	}
	return s.Create(ctx, req)
}

// List returns an audience's notifications, most recent first: the dispatcher feed when member
// is nil, else that rider's inbox.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID, member *uuid.UUID, includeRead bool, limit int) ([]*ent.LogisticsNotification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := s.client.LogisticsNotification.Query().Where(audience(tenantID, member))
	if !includeRead {
		q = q.Where(logisticsnotification.IsRead(false))
	}
	return q.
		Order(ent.Desc(logisticsnotification.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
}

// UnreadCount returns how many unread notifications an audience has.
func (s *Service) UnreadCount(ctx context.Context, tenantID uuid.UUID, member *uuid.UUID) (int, error) {
	return s.client.LogisticsNotification.Query().
		Where(audience(tenantID, member), logisticsnotification.IsRead(false)).
		Count(ctx)
}

// MarkRead marks one of the audience's notifications read. A rider cannot read a dispatcher
// alert or another rider's message, and the dispatcher feed cannot mark a rider's message.
func (s *Service) MarkRead(ctx context.Context, tenantID uuid.UUID, member *uuid.UUID, id uuid.UUID) error {
	n, err := s.client.LogisticsNotification.Update().
		Where(logisticsnotification.ID(id), audience(tenantID, member)).
		SetIsRead(true).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("notifications: mark read: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("notifications: not found")
	}
	return nil
}

// MarkAllRead marks every unread notification of the audience read, returning the count.
func (s *Service) MarkAllRead(ctx context.Context, tenantID uuid.UUID, member *uuid.UUID) (int, error) {
	n, err := s.client.LogisticsNotification.Update().
		Where(audience(tenantID, member), logisticsnotification.IsRead(false)).
		SetIsRead(true).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("notifications: mark all read: %w", err)
	}
	return n, nil
}
