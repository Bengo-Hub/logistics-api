package notifications

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/logisticsnotification"
)

// Service creates and queries operational alerts, and pushes each new one to any connected
// dispatcher via the Hub.
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

// CreateRequest describes a new alert to record and broadcast.
type CreateRequest struct {
	TenantID         uuid.UUID
	NotificationType string
	Title            string
	Body             string
	Payload          map[string]any
	RelatedTaskID    *uuid.UUID
}

// Create persists a new notification and pushes it to any dispatcher currently connected
// for the tenant.
func (s *Service) Create(ctx context.Context, req CreateRequest) (*ent.LogisticsNotification, error) {
	c := s.client.LogisticsNotification.Create().
		SetTenantID(req.TenantID).
		SetNotificationType(req.NotificationType).
		SetTitle(req.Title)
	if req.Body != "" {
		c = c.SetBody(req.Body)
	}
	if req.Payload != nil {
		c = c.SetPayload(req.Payload)
	}
	if req.RelatedTaskID != nil {
		c = c.SetRelatedTaskID(*req.RelatedTaskID)
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

// CreateDeduped is Create, but skips creating a new row (and returns nil, nil) if an unread
// notification of the same type and related task already exists — used by triggers that can
// fire repeatedly for the same underlying condition (e.g. the SLA monitor's periodic scan),
// so a dispatcher gets one alert per real incident rather than one per tick.
func (s *Service) CreateDeduped(ctx context.Context, req CreateRequest) (*ent.LogisticsNotification, error) {
	if req.RelatedTaskID != nil {
		exists, err := s.client.LogisticsNotification.Query().
			Where(
				logisticsnotification.TenantID(req.TenantID),
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

// List returns a tenant's notifications, most recent first.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID, includeRead bool, limit int) ([]*ent.LogisticsNotification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := s.client.LogisticsNotification.Query().
		Where(logisticsnotification.TenantID(tenantID))
	if !includeRead {
		q = q.Where(logisticsnotification.IsRead(false))
	}
	return q.
		Order(ent.Desc(logisticsnotification.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
}

// MarkRead marks a single notification read.
func (s *Service) MarkRead(ctx context.Context, tenantID, id uuid.UUID) error {
	n, err := s.client.LogisticsNotification.Query().
		Where(logisticsnotification.ID(id), logisticsnotification.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("notifications: not found")
		}
		return fmt.Errorf("notifications: query: %w", err)
	}
	if _, err := s.client.LogisticsNotification.UpdateOne(n).SetIsRead(true).Save(ctx); err != nil {
		return fmt.Errorf("notifications: mark read: %w", err)
	}
	return nil
}

// MarkAllRead marks every unread notification for the tenant read, returning the count.
func (s *Service) MarkAllRead(ctx context.Context, tenantID uuid.UUID) (int, error) {
	n, err := s.client.LogisticsNotification.Update().
		Where(logisticsnotification.TenantID(tenantID), logisticsnotification.IsRead(false)).
		SetIsRead(true).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("notifications: mark all read: %w", err)
	}
	return n, nil
}
