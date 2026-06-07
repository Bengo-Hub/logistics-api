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

	"github.com/bengobox/logistics-service/internal/modules/dispatch"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

const (
	transferCreatedConsumer   = "logistics-service-transfer-created"
	transferCreatedAckWait    = 30 * time.Second
	transferCreatedMaxDeliver = 5
)

// TransferReadyConsumer subscribes to inventory.transfer.created and creates
// warehouse-to-warehouse transfer tasks in the logistics service.
type TransferReadyConsumer struct {
	log        *zap.Logger
	taskSvc    *tasks.Service
	dispatcher *dispatch.AutoDispatcher
	svcCtx     context.Context //nolint:containedctx
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

// transferCreatedEvent is the shared-events envelope for inventory.transfer.created.
type transferCreatedEvent struct {
	ID            string `json:"id"`
	EventType     string `json:"event_type"`
	AggregateType string `json:"aggregate_type"`
	TenantID      string `json:"tenant_id"`
	Payload       struct {
		TransferID      string  `json:"transfer_id"`
		TransferNumber  string  `json:"transfer_number"`
		FromWarehouseID string  `json:"from_warehouse_id"`
		FromWarehouse   string  `json:"from_warehouse_name"`
		FromAddress     string  `json:"from_warehouse_address"`
		FromLat         float64 `json:"from_warehouse_lat"`
		FromLng         float64 `json:"from_warehouse_lng"`
		ToWarehouseID   string  `json:"to_warehouse_id"`
		ToWarehouse     string  `json:"to_warehouse_name"`
		ToAddress       string  `json:"to_warehouse_address"`
		ToLat           float64 `json:"to_warehouse_lat"`
		ToLng           float64 `json:"to_warehouse_lng"`
		ItemCount       int     `json:"item_count"`
		Notes           string  `json:"notes"`
	} `json:"payload"`
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

	transferID := evt.Payload.TransferID
	if transferID == "" {
		c.log.Warn("transfer created: missing transfer_id")
		_ = msg.Ack()
		return
	}

	// Build metadata with transfer context
	metadata := map[string]any{
		"transfer_id":       transferID,
		"transfer_number":   evt.Payload.TransferNumber,
		"from_warehouse_id": evt.Payload.FromWarehouseID,
		"to_warehouse_id":   evt.Payload.ToWarehouseID,
		"item_count":        evt.Payload.ItemCount,
		"notes":             evt.Payload.Notes,
	}

	if evt.Payload.FromLat != 0 {
		metadata["pickup_lat"] = evt.Payload.FromLat
		metadata["pickup_lng"] = evt.Payload.FromLng
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
	if evt.Payload.FromLat != 0 && evt.Payload.FromLng != 0 {
		_, stepErr := c.taskSvc.Client().TaskStep.Create().
			SetTaskID(t.ID).
			SetStepType("pickup").
			SetSequence(1).
			SetLocationName(evt.Payload.FromWarehouse).
			SetAddressJSON(map[string]any{
				"latitude":  evt.Payload.FromLat,
				"longitude": evt.Payload.FromLng,
				"name":      evt.Payload.FromWarehouse,
				"address":   evt.Payload.FromAddress,
			}).
			SetMetadata(map[string]any{"warehouse_id": evt.Payload.FromWarehouseID}).
			Save(ctx)
		if stepErr != nil {
			c.log.Warn("failed to create pickup step for transfer", zap.Error(stepErr))
		}
	}

	// Create dropoff step (destination warehouse)
	if evt.Payload.ToLat != 0 && evt.Payload.ToLng != 0 {
		_, stepErr := c.taskSvc.Client().TaskStep.Create().
			SetTaskID(t.ID).
			SetStepType("dropoff").
			SetSequence(2).
			SetLocationName(evt.Payload.ToWarehouse).
			SetAddressJSON(map[string]any{
				"latitude":  evt.Payload.ToLat,
				"longitude": evt.Payload.ToLng,
				"name":      evt.Payload.ToWarehouse,
				"address":   evt.Payload.ToAddress,
			}).
			SetMetadata(map[string]any{"warehouse_id": evt.Payload.ToWarehouseID}).
			Save(ctx)
		if stepErr != nil {
			c.log.Warn("failed to create dropoff step for transfer", zap.Error(stepErr))
		}
	}

	c.log.Info("transfer task created from inventory event",
		zap.String("task_id", t.ID.String()),
		zap.String("transfer_id", transferID),
		zap.String("from", evt.Payload.FromWarehouse),
		zap.String("to", evt.Payload.ToWarehouse),
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
