package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent/shipment"
	"github.com/bengobox/logistics-service/internal/modules/dispatch"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

const (
	transferCreatedConsumer   = "logistics-service-transfer-created"
	transferCreatedAckWait    = 30 * time.Second
	transferCreatedMaxDeliver = 5
)

// TransferReadyConsumer subscribes to inventory.transfer.created and creates
// warehouse-to-warehouse transfer tasks + a distribution Shipment in the logistics service.
type TransferReadyConsumer struct {
	log        *zap.Logger
	taskSvc    *tasks.Service
	dispatcher *dispatch.AutoDispatcher
	svcCtx     context.Context //nolint:containedctx
	// hasFeature gates inventory→logistics distribution sync by subscription entitlement.
	// Nil → fail open.
	hasFeature func(ctx context.Context, tenantID, feature string) bool
}

// NewTransferReadyConsumer creates the consumer.
func NewTransferReadyConsumer(log *zap.Logger, taskSvc *tasks.Service, dispatcher *dispatch.AutoDispatcher) *TransferReadyConsumer {
	return &TransferReadyConsumer{
		log:        log.Named("consumers.transfer_ready"),
		taskSvc:    taskSvc,
		dispatcher: dispatcher,
		svcCtx:     context.Background(),
	}
}

// SetFeatureGate wires the subscription entitlement check used to gate transfer-task sync.
func (c *TransferReadyConsumer) SetFeatureGate(fn func(ctx context.Context, tenantID, feature string) bool) {
	c.hasFeature = fn
}

// entitled reports whether the tenant may have a distribution/transfer task synced. Fails open
// when no gate is wired or tenantID is empty.
func (c *TransferReadyConsumer) entitled(ctx context.Context, tenantID, feature string) bool {
	if c.hasFeature == nil || tenantID == "" {
		return true
	}
	return c.hasFeature(ctx, tenantID, feature)
}

// Start begins consuming inventory.transfer.created via JetStream.
func (c *TransferReadyConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	c.svcCtx = ctx
	eventslib.SubscribeWithRebind(
		c.log,
		js,
		"inventory.transfer.created",
		c.handleMessage,
		nats.Durable(transferCreatedConsumer),
		nats.AckExplicit(),
		nats.AckWait(transferCreatedAckWait),
		nats.MaxDeliver(transferCreatedMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("transfer ready consumer started", zap.String("durable", transferCreatedConsumer))

	<-ctx.Done()
	return nil
}

// warehouseRef mirrors the nested from_warehouse/to_warehouse objects emitted by inventory-api.
type warehouseRef struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Code      string   `json:"code"`
	Address   string   `json:"address"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

// transferPayload mirrors the payload of inventory.transfer.created.
// NOTE: inventory-api emits the warehouses as NESTED objects (from_warehouse / to_warehouse)
// and the lines as an items[] array — this matches that contract exactly.
type transferPayload struct {
	TransferID      string           `json:"transfer_id"`
	TransferNumber  string           `json:"transfer_number"`
	ReferenceNo     string           `json:"reference_no"`
	ShippingCharges float64          `json:"shipping_charges"`
	Carrier         string           `json:"carrier"`
	FreightNotes    string           `json:"freight_notes"`
	Status          string           `json:"status"`
	Notes           string           `json:"notes"`
	FromWarehouse   warehouseRef     `json:"from_warehouse"`
	ToWarehouse     warehouseRef     `json:"to_warehouse"`
	Items           []map[string]any `json:"items"`
}

// transferCreatedEvent is the shared-events envelope for inventory.transfer.created.
type transferCreatedEvent struct {
	EventType     string          `json:"event_type"`
	AggregateType string          `json:"aggregate_type"`
	TenantID      string          `json:"tenant_id"`
	Payload       transferPayload `json:"payload"`
}

func derefFloat(p *float64) float64 {
	if p != nil {
		return *p
	}
	return 0
}

func (c *TransferReadyConsumer) handleMessage(msg *nats.Msg) {
	ctx := c.svcCtx

	var evt transferCreatedEvent
	if err := json.Unmarshal(msg.Data, &evt); err != nil {
		c.log.Warn("transfer created: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	tenantID, err := uuid.Parse(evt.TenantID)
	if err != nil {
		c.log.Warn("transfer created: invalid tenant_id", zap.String("raw", evt.TenantID))
		_ = msg.Ack()
		return
	}

	p := evt.Payload
	transferID := p.TransferID
	if transferID == "" {
		c.log.Warn("transfer created: missing transfer_id")
		_ = msg.Ack()
		return
	}

	// Gate the cross-service distribution sync on the core logistics entitlement. Fails open on a
	// subscriptions-api outage. Warehouse transfers are an optional distribution capability, so a
	// tenant without logistics access never accrues transfer tasks/shipments.
	if !c.entitled(ctx, evt.TenantID, "basic_logistics_access") {
		c.log.Debug("transfer created: tenant lacks basic_logistics_access — skipping transfer task sync",
			zap.String("tenant_id", evt.TenantID), zap.String("transfer_id", transferID))
		_ = msg.Ack()
		return
	}

	fromLat, fromLng := derefFloat(p.FromWarehouse.Latitude), derefFloat(p.FromWarehouse.Longitude)
	toLat, toLng := derefFloat(p.ToWarehouse.Latitude), derefFloat(p.ToWarehouse.Longitude)
	itemCount := len(p.Items)

	// Build metadata with transfer context
	metadata := map[string]any{
		"transfer_id":       transferID,
		"transfer_number":   p.TransferNumber,
		"reference_no":      p.ReferenceNo,
		"carrier":           p.Carrier,
		"shipping_charges":  p.ShippingCharges,
		"from_warehouse_id": p.FromWarehouse.ID,
		"to_warehouse_id":   p.ToWarehouse.ID,
		"item_count":        itemCount,
		"notes":             p.Notes,
	}
	if fromLat != 0 {
		metadata["pickup_lat"] = fromLat
		metadata["pickup_lng"] = fromLng
	}

	// Create transfer task (idempotent via external_reference)
	externalRef := fmt.Sprintf("transfer:%s", transferID)
	t, err := c.taskSvc.CreateTask(ctx, tenantID, tasks.CreateTaskRequest{
		ExternalReference: externalRef,
		SourceService:     "inventory",
		TaskType:          "outlet_transfer",
		Metadata:          metadata,
	})
	if err != nil {
		c.log.Error("transfer created: create task failed",
			zap.Error(err),
			zap.String("transfer_id", transferID),
		)
		_ = msg.Nak()
		return
	}

	// Create pickup step (source warehouse)
	if fromLat != 0 && fromLng != 0 {
		_, stepErr := c.taskSvc.Client().TaskStep.Create().
			SetTaskID(t.ID).
			SetStepType("pickup").
			SetSequence(1).
			SetLocationName(p.FromWarehouse.Name).
			SetAddressJSON(map[string]any{
				"latitude":  fromLat,
				"longitude": fromLng,
				"name":      p.FromWarehouse.Name,
				"address":   p.FromWarehouse.Address,
			}).
			SetMetadata(map[string]any{"warehouse_id": p.FromWarehouse.ID}).
			Save(ctx)
		if stepErr != nil {
			c.log.Warn("failed to create pickup step for transfer", zap.Error(stepErr))
		}
	}

	// Create dropoff step (destination warehouse)
	if toLat != 0 && toLng != 0 {
		_, stepErr := c.taskSvc.Client().TaskStep.Create().
			SetTaskID(t.ID).
			SetStepType("dropoff").
			SetSequence(2).
			SetLocationName(p.ToWarehouse.Name).
			SetAddressJSON(map[string]any{
				"latitude":  toLat,
				"longitude": toLng,
				"name":      p.ToWarehouse.Name,
				"address":   p.ToWarehouse.Address,
			}).
			SetMetadata(map[string]any{"warehouse_id": p.ToWarehouse.ID}).
			Save(ctx)
		if stepErr != nil {
			c.log.Warn("failed to create dropoff step for transfer", zap.Error(stepErr))
		}
	}

	// Create the distribution Shipment envelope (batch + seal + chain-of-custody anchor) for the
	// transfer. Idempotent via external_reference so redelivery never duplicates.
	c.ensureShipment(ctx, tenantID, &p, externalRef)

	c.log.Info("transfer task created from inventory event",
		zap.String("task_id", t.ID.String()),
		zap.String("transfer_id", transferID),
		zap.String("from", p.FromWarehouse.Name),
		zap.String("to", p.ToWarehouse.Name),
	)

	// Auto-dispatch if configured — derive from svcCtx (not Background) so the
	// goroutine is cancelled when the service shuts down.
	if c.dispatcher != nil {
		dispatchCtx, dispatchCancel := context.WithTimeout(c.svcCtx, 30*time.Second)
		go func() {
			defer dispatchCancel()
			if dispatchErr := c.dispatcher.DispatchTask(dispatchCtx, tenantID, t.ID); dispatchErr != nil {
				c.log.Warn("auto-dispatch failed for transfer task",
					zap.String("task_id", t.ID.String()),
					zap.Error(dispatchErr),
				)
			}
		}()
	}

	_ = msg.Ack()
}

// ensureShipment creates a distribution Shipment for the transfer if one does not already exist
// (idempotent on external_reference). Best-effort: a failure here never fails the transfer task.
func (c *TransferReadyConsumer) ensureShipment(ctx context.Context, tenantID uuid.UUID, p *transferPayload, externalRef string) {
	client := c.taskSvc.Client()
	exists, err := client.Shipment.Query().
		Where(shipment.TenantID(tenantID), shipment.ExternalReference(externalRef)).
		Exist(ctx)
	if err != nil {
		c.log.Warn("shipment idempotency check failed", zap.Error(err))
		return
	}
	if exists {
		return
	}

	code := fmt.Sprintf("SHIP-%d-%s", time.Now().Year(), uuid.New().String()[:6])
	create := client.Shipment.Create().
		SetTenantID(tenantID).
		SetShipmentCode(code).
		SetShipmentType("warehouse_transfer").
		SetStatus("planned").
		SetExternalReference(externalRef).
		SetSourceFacilityName(p.FromWarehouse.Name).
		SetDestFacilityName(p.ToWarehouse.Name).
		SetMetadata(map[string]any{
			"transfer_id":      p.TransferID,
			"transfer_number":  p.TransferNumber,
			"reference_no":     p.ReferenceNo,
			"carrier":          p.Carrier,
			"shipping_charges": p.ShippingCharges,
			"freight_notes":    p.FreightNotes,
			"item_count":       len(p.Items),
		})
	if id, e := uuid.Parse(p.FromWarehouse.ID); e == nil {
		create = create.SetSourceFacilityID(id)
	}
	if id, e := uuid.Parse(p.ToWarehouse.ID); e == nil {
		create = create.SetDestFacilityID(id)
	}

	if _, err := create.Save(ctx); err != nil {
		c.log.Warn("failed to create shipment for transfer", zap.Error(err), zap.String("transfer_id", p.TransferID))
		return
	}
	c.log.Info("distribution shipment created for transfer",
		zap.String("shipment_code", code),
		zap.String("transfer_id", p.TransferID),
	)
}
