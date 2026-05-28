package tenant

import (
	"context"
	"fmt"
	"time"

	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	enttenant "github.com/bengobox/logistics-service/internal/ent/tenant"
)

// TenantEventSubscriber mirrors auth.tenant.created/updated NATS events into the
// local tenants table, keeping tenant metadata in sync without HTTP round-trips.
type TenantEventSubscriber struct {
	client *ent.Client
	logger *zap.Logger
}

// NewTenantEventSubscriber constructs a TenantEventSubscriber.
func NewTenantEventSubscriber(client *ent.Client, logger *zap.Logger) *TenantEventSubscriber {
	return &TenantEventSubscriber{
		client: client,
		logger: logger.Named("tenant.event_subscriber"),
	}
}

// Start subscribes to auth.tenant.created and auth.tenant.updated via JetStream durable consumers.
func (s *TenantEventSubscriber) Start(nc *nats.Conn) error {
	if nc == nil {
		s.logger.Warn("NATS not available, skipping tenant event subscriptions")
		return nil
	}

	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("tenant event subscriber: jetstream init: %w", err)
	}

	ctx := context.Background()

	type sub struct {
		subject string
		durable string
	}
	subs := []sub{
		{"auth.tenant.created", "logistics-auth-tenant-created"},
		{"auth.tenant.updated", "logistics-auth-tenant-updated"},
	}

	for _, cfg := range subs {
		cfg := cfg
		cons, err := js.CreateOrUpdateConsumer(ctx, authStream, jetstream.ConsumerConfig{
			Durable:       cfg.durable,
			FilterSubject: cfg.subject,
			AckPolicy:     jetstream.AckExplicitPolicy,
			AckWait:       30 * time.Second,
			MaxDeliver:    5,
			DeliverPolicy: jetstream.DeliverAllPolicy,
		})
		if err != nil {
			s.logger.Warn("tenant event subscriber: create consumer failed",
				zap.String("durable", cfg.durable), zap.Error(err))
			continue
		}

		if _, err := cons.Consume(func(msg jetstream.Msg) {
			evt, parseErr := sharedevents.FromJSON(msg.Data())
			if parseErr != nil {
				s.logger.Error("failed to parse tenant event",
					zap.String("subject", cfg.subject), zap.Error(parseErr))
				_ = msg.Nak()
				return
			}
			if handleErr := s.handleUpsert(context.Background(), evt); handleErr != nil {
				s.logger.Error("failed to handle tenant event",
					zap.String("subject", cfg.subject), zap.Error(handleErr))
				_ = msg.Nak()
				return
			}
			_ = msg.Ack()
		}); err != nil {
			s.logger.Warn("tenant event subscriber: consume failed",
				zap.String("durable", cfg.durable), zap.Error(err))
		}
	}

	s.logger.Info("tenant event subscriptions active",
		zap.String("subjects", "auth.tenant.created, auth.tenant.updated"))
	return nil
}

// handleUpsert creates or updates a local tenant mirror from auth.tenant.created/updated.
func (s *TenantEventSubscriber) handleUpsert(ctx context.Context, evt *sharedevents.Event) error {
	tenantIDStr, _ := evt.Payload["tenant_id"].(string)
	name, _ := evt.Payload["name"].(string)
	slug, _ := evt.Payload["slug"].(string)
	status, _ := evt.Payload["status"].(string)
	useCase, _ := evt.Payload["use_case"].(string)

	if slug == "" {
		return fmt.Errorf("missing slug in tenant event")
	}
	if status == "" {
		status = "active"
	}

	tenantID, err := uuid.Parse(tenantIDStr)
	if err != nil {
		return fmt.Errorf("invalid tenant_id %q: %w", tenantIDStr, err)
	}

	now := time.Now()

	err = s.client.Tenant.Create().
		SetID(tenantID).
		SetName(name).
		SetSlug(slug).
		SetStatus(status).
		SetNillableUseCase(nillableStr(useCase)).
		SetSyncStatus("synced").
		SetLastSyncAt(now).
		OnConflictColumns(enttenant.FieldID).
		UpdateNewValues().
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("upsert tenant from event: %w", err)
	}

	s.logger.Debug("tenant synced from auth event",
		zap.String("slug", slug), zap.String("tenant_id", tenantIDStr))
	return nil
}
