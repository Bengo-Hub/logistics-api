package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/user"
)

// RoleDriver is the universal role for all delivery/courier/taxi/ride use cases.
const RoleDriver = "driver"

// AuthUserCreatedEvent represents the payload from auth.user.created events.
type AuthUserCreatedEvent struct {
	UserID     string   `json:"user_id"`
	Email      string   `json:"email"`
	FullName   string   `json:"full_name"`
	Phone      string   `json:"phone"`
	TenantID   string   `json:"tenant_id"`
	TenantSlug string   `json:"tenant_slug"`
	Roles      []string `json:"roles"`
	Method     string   `json:"method"`
}

// AuthUserUpdatedEvent represents the payload from auth.user.updated events.
type AuthUserUpdatedEvent struct {
	UserID     string   `json:"user_id"`
	Email      string   `json:"email"`
	FullName   string   `json:"full_name"`
	Phone      string   `json:"phone"`
	TenantID   string   `json:"tenant_id"`
	TenantSlug string   `json:"tenant_slug"`
	Roles      []string `json:"roles"`
	Status     string   `json:"status"`
}

// EventHandler handles auth-service events for identity sync.
type EventHandler struct {
	service *Service
	logger  *zap.Logger
}

// NewEventHandler creates a new identity event handler.
func NewEventHandler(service *Service, logger *zap.Logger) *EventHandler {
	return &EventHandler{
		service: service,
		logger:  logger.Named("identity.events"),
	}
}

// SubscribeToAuthEvents subscribes to auth-service events via NATS.
func (h *EventHandler) SubscribeToAuthEvents(nc *nats.Conn) error {
	// Subscribe to auth.user.created
	_, err := nc.Subscribe("auth.user.created", func(msg *nats.Msg) {
		var evt AuthUserCreatedEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			h.logger.Error("failed to unmarshal auth.user.created event", zap.Error(err))
			return
		}

		ctx := context.Background()
		if err := h.handleUserCreated(ctx, &evt); err != nil {
			h.logger.Error("failed to handle auth.user.created event", zap.Error(err))
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("identity: subscribe to auth.user.created: %w", err)
	}

	// Subscribe to auth.user.updated
	_, err = nc.Subscribe("auth.user.updated", func(msg *nats.Msg) {
		var evt AuthUserUpdatedEvent
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			h.logger.Error("failed to unmarshal auth.user.updated event", zap.Error(err))
			return
		}

		ctx := context.Background()
		if err := h.handleUserUpdated(ctx, &evt); err != nil {
			h.logger.Error("failed to handle auth.user.updated event", zap.Error(err))
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("identity: subscribe to auth.user.updated: %w", err)
	}

	h.logger.Info("auth event subscriptions active",
		zap.String("subjects", "auth.user.created, auth.user.updated"))
	return nil
}

// handleUserCreated syncs a user from auth.user.created event.
// If the user was previously invited (stub with status="invited"), it links the auth_service_user_id
// and updates the user to synced status.
func (h *EventHandler) handleUserCreated(ctx context.Context, evt *AuthUserCreatedEvent) error {
	authUserID, err := uuid.Parse(evt.UserID)
	if err != nil {
		return fmt.Errorf("invalid user_id in event: %w", err)
	}

	tenantID, err := h.service.tenantSyncer.SyncTenant(ctx, evt.TenantSlug)
	if err != nil {
		return fmt.Errorf("sync tenant %s: %w", evt.TenantSlug, err)
	}

	// Check if user already exists by auth_service_user_id
	existing, _ := h.service.client.User.Query().
		Where(user.AuthServiceUserID(authUserID)).
		First(ctx)
	if existing != nil {
		// Already synced
		return nil
	}

	// Check if there's a stub user from rider invitation (matched by email + tenant)
	stub, _ := h.service.client.User.Query().
		Where(user.TenantID(tenantID), user.Email(evt.Email)).
		First(ctx)

	role := resolveRole(evt.Roles, evt.TenantSlug)

	if stub != nil {
		// Link the stub to the real auth-service user
		_, err = h.service.client.User.UpdateOne(stub).
			SetAuthServiceUserID(authUserID).
			SetFullName(coalesce(evt.FullName, stub.FullName)).
			SetStatus("active").
			SetSyncStatus("synced").
			SetSyncAt(time.Now()).
			SetRole(role).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("link stub user to auth: %w", err)
		}
		log.Printf("[identity-event] linked stub user %s to auth_service_user_id=%s role=%s", evt.Email, authUserID, role)
		return nil
	}

	// Create new user
	_, err = h.service.client.User.Create().
		SetAuthServiceUserID(authUserID).
		SetTenantID(tenantID).
		SetEmail(evt.Email).
		SetFullName(coalesce(evt.FullName, evt.Email)).
		SetPhone(evt.Phone).
		SetStatus("active").
		SetSyncStatus("synced").
		SetSyncAt(time.Now()).
		SetRole(role).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			// Already exists (race condition), skip
			return nil
		}
		return fmt.Errorf("create user from event: %w", err)
	}

	log.Printf("[identity-event] created user %s (auth=%s) tenant=%s role=%s", evt.Email, authUserID, evt.TenantSlug, role)
	return nil
}

// handleUserUpdated updates a local user from auth.user.updated event.
func (h *EventHandler) handleUserUpdated(ctx context.Context, evt *AuthUserUpdatedEvent) error {
	authUserID, err := uuid.Parse(evt.UserID)
	if err != nil {
		return fmt.Errorf("invalid user_id in event: %w", err)
	}

	existing, _ := h.service.client.User.Query().
		Where(user.AuthServiceUserID(authUserID)).
		First(ctx)
	if existing == nil {
		// User not found locally, skip
		return nil
	}

	builder := h.service.client.User.UpdateOne(existing).
		SetSyncStatus("synced").
		SetSyncAt(time.Now())

	if evt.FullName != "" {
		builder.SetFullName(evt.FullName)
	}
	if evt.Phone != "" {
		builder.SetPhone(evt.Phone)
	}
	if evt.Status != "" {
		builder.SetStatus(evt.Status)
	}
	if len(evt.Roles) > 0 {
		builder.SetRole(resolveRole(evt.Roles, evt.TenantSlug))
	}

	if _, err := builder.Save(ctx); err != nil {
		return fmt.Errorf("update user from event: %w", err)
	}

	return nil
}

// resolveRole determines the service-level role from auth-service roles.
// Uses "driver" as the universal role for delivery/courier/taxi use cases.
func resolveRole(roles []string, tenantSlug string) string {
	for _, r := range roles {
		switch r {
		case "superuser", "admin":
			return "admin"
		case "driver", "rider":
			return RoleDriver
		case "staff":
			return "staff"
		}
	}
	// Default: if user registered for a tenant with a fleet invitation, they're a driver
	return RoleDriver
}

func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
