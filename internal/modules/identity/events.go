package identity

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"

	sharedevents "github.com/Bengo-Hub/shared-events"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/user"
)

// RoleDriver is the universal role for all delivery/courier/taxi/ride use cases.
const RoleDriver = "driver"

// authStream is the JetStream stream name that auth-api publishes to.
const authStream = "auth"

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

// ensureAuthStream creates the auth stream if it does not already exist.
// This guards against startup races where the stream may not yet be present.
func ensureAuthStream(ctx context.Context, js jetstream.JetStream) error {
	_, err := js.Stream(ctx, authStream)
	if err == nil {
		// Stream already exists — nothing to do.
		return nil
	}
	if !errors.Is(err, jetstream.ErrStreamNotFound) {
		return fmt.Errorf("identity: lookup auth stream: %w", err)
	}

	_, err = js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     authStream,
		Subjects: []string{"auth.>"},
		MaxAge:   72 * time.Hour,
		Storage:  jetstream.FileStorage,
	})
	if err != nil {
		// Another instance may have created it concurrently — treat that as success.
		if errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
			return nil
		}
		return fmt.Errorf("identity: create auth stream: %w", err)
	}
	return nil
}

// SubscribeToAuthEvents subscribes to auth-service events via JetStream durable consumers.
func (h *EventHandler) SubscribeToAuthEvents(nc *nats.Conn) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("identity: init JetStream: %w", err)
	}

	ctx := context.Background()

	if err := ensureAuthStream(ctx, js); err != nil {
		return err
	}

	// --- auth.user.created ---
	createdCons, err := js.CreateOrUpdateConsumer(ctx, authStream, jetstream.ConsumerConfig{
		Durable:       "log-auth-user-created",
		FilterSubject: "auth.user.created",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return fmt.Errorf("identity: create consumer log-auth-user-created: %w", err)
	}

	_, err = createdCons.Consume(func(msg jetstream.Msg) {
		evt, parseErr := sharedevents.FromJSON(msg.Data())
		if parseErr != nil {
			h.logger.Error("failed to parse auth.user.created envelope", zap.Error(parseErr))
			_ = msg.Nak()
			return
		}

		if handleErr := h.handleUserCreated(context.Background(), evt); handleErr != nil {
			h.logger.Error("failed to handle auth.user.created event", zap.Error(handleErr))
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("identity: consume log-auth-user-created: %w", err)
	}

	// --- auth.user.updated ---
	updatedCons, err := js.CreateOrUpdateConsumer(ctx, authStream, jetstream.ConsumerConfig{
		Durable:       "log-auth-user-updated",
		FilterSubject: "auth.user.updated",
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return fmt.Errorf("identity: create consumer log-auth-user-updated: %w", err)
	}

	_, err = updatedCons.Consume(func(msg jetstream.Msg) {
		evt, parseErr := sharedevents.FromJSON(msg.Data())
		if parseErr != nil {
			h.logger.Error("failed to parse auth.user.updated envelope", zap.Error(parseErr))
			_ = msg.Nak()
			return
		}

		if handleErr := h.handleUserUpdated(context.Background(), evt); handleErr != nil {
			h.logger.Error("failed to handle auth.user.updated event", zap.Error(handleErr))
			_ = msg.Nak()
			return
		}
		_ = msg.Ack()
	})
	if err != nil {
		return fmt.Errorf("identity: consume log-auth-user-updated: %w", err)
	}

	h.logger.Info("auth JetStream durable consumers active",
		zap.String("stream", authStream),
		zap.Strings("consumers", []string{"log-auth-user-created", "log-auth-user-updated"}))
	return nil
}

// handleUserCreated syncs a user from auth.user.created event.
// If the user was previously invited (stub with status="invited"), it links the auth_service_user_id
// and updates the user to synced status.
func (h *EventHandler) handleUserCreated(ctx context.Context, evt *sharedevents.Event) error {
	userIDStr, _ := evt.Payload["user_id"].(string)
	authUserID, err := uuid.Parse(userIDStr)
	if err != nil {
		return fmt.Errorf("invalid user_id in event: %w", err)
	}

	tenantSlug, _ := evt.Payload["tenant_slug"].(string)
	email, _ := evt.Payload["email"].(string)
	fullName, _ := evt.Payload["full_name"].(string)
	phone, _ := evt.Payload["phone"].(string)

	rolesRaw, _ := evt.Payload["roles"].([]interface{})
	roles := make([]string, 0, len(rolesRaw))
	for _, r := range rolesRaw {
		if s, ok := r.(string); ok {
			roles = append(roles, s)
		}
	}

	tenantID, err := h.service.tenantSyncer.SyncTenant(ctx, tenantSlug)
	if err != nil {
		return fmt.Errorf("sync tenant %s: %w", tenantSlug, err)
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
		Where(user.TenantID(tenantID), user.Email(email)).
		First(ctx)

	role := resolveRole(roles, tenantSlug)

	if stub != nil {
		// Link the stub to the real auth-service user
		_, err = h.service.client.User.UpdateOne(stub).
			SetAuthServiceUserID(authUserID).
			SetFullName(coalesce(fullName, stub.FullName)).
			SetStatus("active").
			SetSyncStatus("synced").
			SetSyncAt(time.Now()).
			SetRole(role).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("link stub user to auth: %w", err)
		}
		log.Printf("[identity-event] linked stub user %s to auth_service_user_id=%s role=%s", email, authUserID, role)
		return nil
	}

	// Create new user
	_, err = h.service.client.User.Create().
		SetAuthServiceUserID(authUserID).
		SetTenantID(tenantID).
		SetEmail(email).
		SetFullName(coalesce(fullName, email)).
		SetPhone(phone).
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

	log.Printf("[identity-event] created user %s (auth=%s) tenant=%s role=%s", email, authUserID, tenantSlug, role)
	return nil
}

// handleUserUpdated updates a local user from auth.user.updated event.
func (h *EventHandler) handleUserUpdated(ctx context.Context, evt *sharedevents.Event) error {
	userIDStr, _ := evt.Payload["user_id"].(string)
	authUserID, err := uuid.Parse(userIDStr)
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

	fullName, _ := evt.Payload["full_name"].(string)
	phone, _ := evt.Payload["phone"].(string)
	status, _ := evt.Payload["status"].(string)
	tenantSlug, _ := evt.Payload["tenant_slug"].(string)

	rolesRaw, _ := evt.Payload["roles"].([]interface{})
	roles := make([]string, 0, len(rolesRaw))
	for _, r := range rolesRaw {
		if s, ok := r.(string); ok {
			roles = append(roles, s)
		}
	}

	builder := h.service.client.User.UpdateOne(existing).
		SetSyncStatus("synced").
		SetSyncAt(time.Now())

	if fullName != "" {
		builder.SetFullName(fullName)
	}
	if phone != "" {
		builder.SetPhone(phone)
	}
	if status != "" {
		builder.SetStatus(status)
	}
	if len(roles) > 0 {
		builder.SetRole(resolveRole(roles, tenantSlug))
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
