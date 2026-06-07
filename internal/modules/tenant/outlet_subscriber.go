package tenant

import (
	"context"
	"fmt"
	"time"

	sharedevents "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	entoutlet "github.com/bengobox/logistics-service/internal/ent/outlet"
)

const authStream = "auth"

// OutletSubscriber syncs auth.outlet.* JetStream events from auth-api into the
// local logistics-api outlets table. Only outlets with use_case="logistics" are
// accepted; all others are ACKed and skipped.
type OutletSubscriber struct {
	client *ent.Client
	logger *zap.Logger
}

// NewOutletSubscriber constructs an OutletSubscriber.
func NewOutletSubscriber(client *ent.Client, logger *zap.Logger) *OutletSubscriber {
	return &OutletSubscriber{
		client: client,
		logger: logger.Named("tenant.outlet_subscriber"),
	}
}

// Start subscribes to auth.outlet.* via JetStream durable consumers.
func (s *OutletSubscriber) Start(nc *nats.Conn) error {
	if nc == nil {
		s.logger.Warn("NATS not available, skipping outlet event subscriptions")
		return nil
	}

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("outlet subscriber: jetstream init: %w", err)
	}

	// Ensure the auth stream exists (guard against startup race with auth-api).
	if _, err := js.StreamInfo(authStream); err != nil {
		if _, addErr := js.AddStream(&nats.StreamConfig{
			Name:      authStream,
			Subjects:  []string{"auth.>"},
			Retention: nats.LimitsPolicy,
			MaxAge:    72 * time.Hour,
			Storage:   nats.FileStorage,
		}); addErr != nil && addErr != nats.ErrStreamNameAlreadyInUse {
			s.logger.Warn("outlet subscriber: ensure auth stream failed", zap.Error(addErr))
		}
	}

	type sub struct {
		subject string
		durable string
		handler func(context.Context, *sharedevents.Event) error
	}
	subs := []sub{
		{"auth.outlet.created", "logistics-auth-outlet-created", s.handleUpsert},
		{"auth.outlet.updated", "logistics-auth-outlet-updated", s.handleUpsert},
		{"auth.outlet.archived", "logistics-auth-outlet-archived", s.handleArchive},
	}

	for _, cfg := range subs {
		cfg := cfg
		sharedevents.SubscribeWithRebind(s.logger, js, cfg.subject, func(msg *nats.Msg) {
			evt, err := sharedevents.FromJSON(msg.Data)
			if err != nil {
				s.logger.Error("failed to unmarshal outlet event",
					zap.String("subject", cfg.subject), zap.Error(err))
				_ = msg.Nak()
				return
			}
			if err := cfg.handler(context.Background(), evt); err != nil {
				s.logger.Error("failed to handle outlet event",
					zap.String("subject", cfg.subject), zap.Error(err))
				_ = msg.Nak()
				return
			}
			_ = msg.Ack()
		},
			nats.Durable(cfg.durable),
			nats.AckExplicit(),
			nats.AckWait(30*time.Second),
			nats.MaxDeliver(5),
			nats.DeliverAll(),
		)
	}

	s.logger.Info("logistics outlet event subscriptions active",
		zap.String("subjects", "auth.outlet.created, auth.outlet.updated, auth.outlet.archived"))
	return nil
}

// logisticsUseCases are the outlet use_case values that logistics-api manages.
// Auth-api may tag outlets with any of these depending on the tenant's business model.
var logisticsUseCases = map[string]bool{
	"logistics":    true,
	"courier":      true,
	"distribution": true,
	"delivery":     true,
	"fleet":        true,
}

// handleUpsert creates or updates a local outlet mirror from auth.outlet.created/updated.
// Accepts any logistics-adjacent use_case; skips POS/retail/hospitality outlets.
func (s *OutletSubscriber) handleUpsert(ctx context.Context, evt *sharedevents.Event) error {
	outletIDStr, _ := evt.Payload["outlet_id"].(string)
	code, _ := evt.Payload["code"].(string)
	name, _ := evt.Payload["name"].(string)
	useCase, _ := evt.Payload["use_case"].(string)
	isHQ, _ := evt.Payload["is_hq"].(bool)
	status, _ := evt.Payload["status"].(string)
	address, _ := evt.Payload["address"].(string)
	if status == "" {
		status = "active"
	}

	// Accept any logistics-adjacent use_case; skip POS/retail/hospitality outlets.
	// Empty use_case is also accepted (backwards compat with tenants that haven't set it).
	if useCase != "" && !logisticsUseCases[useCase] {
		s.logger.Debug("skipping outlet: not a logistics hub",
			zap.String("outlet_id", outletIDStr),
			zap.String("use_case", useCase))
		return nil
	}

	outletID, err := uuid.Parse(outletIDStr)
	if err != nil {
		return fmt.Errorf("invalid outlet_id %q: %w", outletIDStr, err)
	}
	if evt.TenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in outlet event")
	}

	existing, err := s.client.Outlet.Get(ctx, outletID)
	if err != nil {
		create := s.client.Outlet.Create().
			SetID(outletID).
			SetTenantID(evt.TenantID).
			SetCode(code).
			SetName(name).
			SetUseCase(useCase).
			SetIsHq(isHQ).
			SetStatus(status)
		if address != "" {
			create = create.SetAddress(address)
		}
		if _, createErr := create.Save(ctx); createErr != nil {
			return fmt.Errorf("create logistics outlet mirror: %w", createErr)
		}
		s.logger.Info("logistics outlet created from auth event",
			zap.String("outlet_id", outletIDStr), zap.String("code", code))
		return nil
	}

	upd := s.client.Outlet.UpdateOne(existing).
		SetName(name).
		SetIsHq(isHQ).
		SetStatus(status)
	if address != "" {
		upd = upd.SetAddress(address)
	}
	if _, updErr := upd.Save(ctx); updErr != nil {
		return fmt.Errorf("update logistics outlet mirror: %w", updErr)
	}
	s.logger.Info("logistics outlet updated from auth event",
		zap.String("outlet_id", outletIDStr), zap.String("code", code))
	return nil
}

// handleArchive sets status = "archived" for the outlet.
// If the outlet was never synced (not a logistics hub), this is a no-op.
func (s *OutletSubscriber) handleArchive(ctx context.Context, evt *sharedevents.Event) error {
	outletIDStr, _ := evt.Payload["outlet_id"].(string)
	outletID, err := uuid.Parse(outletIDStr)
	if err != nil {
		return fmt.Errorf("invalid outlet_id %q: %w", outletIDStr, err)
	}

	n, err := s.client.Outlet.Update().
		Where(entoutlet.ID(outletID), entoutlet.TenantID(evt.TenantID)).
		SetStatus("archived").
		Save(ctx)
	if err != nil {
		return fmt.Errorf("archive logistics outlet: %w", err)
	}
	if n > 0 {
		s.logger.Info("logistics outlet archived from auth event",
			zap.String("outlet_id", outletIDStr))
	}
	return nil // n==0 means outlet was never a logistics hub — safe to ignore
}
