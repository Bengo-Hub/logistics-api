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
	enttenant "github.com/bengobox/logistics-service/internal/ent/tenant"
)

const authStream = "auth"

// OutletSubscriber syncs auth.outlet.* JetStream events from auth-api into the
// local logistics-api outlets table. Logistics hubs are always mirrored; outlets of any
// other use case (a cafe, a pharmacy) are mirrored when their tenant exists in logistics,
// because those outlets dispatch deliveries and anchor delivery quotes.
type OutletSubscriber struct {
	client   *ent.Client
	logger   *zap.Logger
	onChange func(ctx context.Context, tenantID uuid.UUID)
}

// OnChange registers a callback run after an outlet is created, moved or archived.
// The zones service uses it to drop cached quotes and coverage for the tenant.
func (s *OutletSubscriber) OnChange(f func(ctx context.Context, tenantID uuid.UUID)) {
	s.onChange = f
}

func (s *OutletSubscriber) changed(ctx context.Context, tenantID uuid.UUID) {
	if s.onChange != nil {
		s.onChange(ctx, tenantID)
	}
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
		sharedevents.SubscribeQueueWithRebind(s.logger, js, authStream, cfg.subject, cfg.durable, func(msg *nats.Msg) {
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

	outletID, err := uuid.Parse(outletIDStr)
	if err != nil {
		return fmt.Errorf("invalid outlet_id %q: %w", outletIDStr, err)
	}
	if evt.TenantID == uuid.Nil {
		return fmt.Errorf("missing tenant_id in outlet event")
	}

	rec := OutletRecord{ID: outletID, TenantID: evt.TenantID, Code: code, Name: name, UseCase: useCase, IsHQ: isHQ, Status: status, Address: address}
	if lat, lng, ok := outletLocation(evt.Payload); ok {
		rec.Latitude, rec.Longitude = &lat, &lng
	}
	changed, err := UpsertOutlet(ctx, s.client, rec)
	if err != nil {
		return err
	}
	if changed {
		s.logger.Info("logistics outlet synced from auth event", zap.String("outlet_id", outletIDStr), zap.String("code", code))
		s.changed(ctx, evt.TenantID)
	}
	return nil
}

// OutletRecord is an auth-api outlet as logistics mirrors it.
type OutletRecord struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	Code      string
	Name      string
	UseCase   string
	IsHQ      bool
	Status    string
	Address   string
	Latitude  *float64
	Longitude *float64
}

// UpsertOutlet creates or updates the local mirror of an auth outlet. It is the one
// outlet write path, shared by the auth.outlet.* subscriber and the startup resync.
//
// Logistics hubs (or an unset use_case) are always mirrored. Any other outlet is mirrored
// only for tenants that exist in logistics, so a shop that offers delivery gets its pickup
// point without pulling in every tenant's branches. A missing pin keeps the stored one.
// Returns whether a row was written.
func UpsertOutlet(ctx context.Context, client *ent.Client, r OutletRecord) (bool, error) {
	if r.ID == uuid.Nil || r.TenantID == uuid.Nil {
		return false, fmt.Errorf("outlet upsert: missing outlet or tenant id")
	}
	if r.UseCase != "" && !logisticsUseCases[r.UseCase] {
		if ok, terr := client.Tenant.Query().Where(enttenant.ID(r.TenantID)).Exist(ctx); terr != nil || !ok {
			return false, nil
		}
	}
	if r.UseCase == "" {
		r.UseCase = "logistics"
	}
	if r.Status == "" {
		r.Status = "active"
	}

	existing, err := client.Outlet.Get(ctx, r.ID)
	if err != nil {
		if !ent.IsNotFound(err) {
			return false, fmt.Errorf("load logistics outlet mirror: %w", err)
		}
		create := client.Outlet.Create().
			SetID(r.ID).
			SetTenantID(r.TenantID).
			SetCode(r.Code).
			SetName(r.Name).
			SetUseCase(r.UseCase).
			SetIsHq(r.IsHQ).
			SetStatus(r.Status)
		if r.Address != "" {
			create = create.SetAddress(r.Address)
		}
		if r.Latitude != nil && r.Longitude != nil {
			create = create.SetLatitude(*r.Latitude).SetLongitude(*r.Longitude)
		}
		if _, err := create.Save(ctx); err != nil {
			return false, fmt.Errorf("create logistics outlet mirror: %w", err)
		}
		return true, nil
	}

	upd := client.Outlet.UpdateOne(existing).
		SetName(r.Name).
		SetUseCase(r.UseCase).
		SetIsHq(r.IsHQ).
		SetStatus(r.Status)
	if r.Code != "" {
		upd = upd.SetCode(r.Code)
	}
	if r.Address != "" {
		upd = upd.SetAddress(r.Address)
	}
	if r.Latitude != nil && r.Longitude != nil {
		upd = upd.SetLatitude(*r.Latitude).SetLongitude(*r.Longitude)
	}
	if _, err := upd.Save(ctx); err != nil {
		return false, fmt.Errorf("update logistics outlet mirror: %w", err)
	}
	return true, nil
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
		s.changed(ctx, evt.TenantID)
	}
	return nil // n==0 means outlet was never a logistics hub — safe to ignore
}

// outletLocation reads the outlet pin from an auth.outlet.* payload. auth-api sends it as
// top-level latitude/longitude; older events may only carry it inside metadata.
func outletLocation(payload map[string]any) (lat, lng float64, ok bool) {
	read := func(m map[string]any) (float64, float64, bool) {
		la, ok1 := m["latitude"].(float64)
		lo, ok2 := m["longitude"].(float64)
		if !ok1 || !ok2 || (la == 0 && lo == 0) || la < -90 || la > 90 || lo < -180 || lo > 180 {
			return 0, 0, false
		}
		return la, lo, true
	}
	if la, lo, ok := read(payload); ok {
		return la, lo, true
	}
	if md, isMap := payload["metadata"].(map[string]any); isMap {
		return read(md)
	}
	return 0, 0, false
}
