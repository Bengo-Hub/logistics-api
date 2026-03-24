package consumers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/dispatch"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

const (
	orderReadyConsumer    = "logistics-service-order-ready"
	orderReadyAckWait     = 30 * time.Second
	orderReadyMaxDeliver  = 5
)

// OrderReadyConsumer subscribes to ordering.order.ready and creates delivery tasks.
type OrderReadyConsumer struct {
	log        *zap.Logger
	taskSvc    *tasks.Service
	dispatcher *dispatch.AutoDispatcher
}

// NewOrderReadyConsumer creates the consumer.
func NewOrderReadyConsumer(log *zap.Logger, taskSvc *tasks.Service, dispatcher *dispatch.AutoDispatcher) *OrderReadyConsumer {
	return &OrderReadyConsumer{
		log:        log.Named("consumers.order_ready"),
		taskSvc:    taskSvc,
		dispatcher: dispatcher,
	}
}

// Start begins consuming ordering.order.ready via JetStream.
func (c *OrderReadyConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	sub, err := js.Subscribe(
		"ordering.order.ready",
		c.handleMessage,
		nats.Durable(orderReadyConsumer),
		nats.AckExplicit(),
		nats.AckWait(orderReadyAckWait),
		nats.MaxDeliver(orderReadyMaxDeliver),
		nats.DeliverAll(),
	)
	if err != nil {
		return fmt.Errorf("order ready consumer: subscribe: %w", err)
	}
	c.log.Info("order ready consumer started", zap.String("durable", orderReadyConsumer))

	<-ctx.Done()
	return sub.Unsubscribe()
}

func (c *OrderReadyConsumer) handleMessage(msg *nats.Msg) {
	ctx := context.Background()

	var envelope struct {
		Payload struct {
			TenantID string `json:"tenant_id"`
			OrderID  string `json:"order_id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("order ready: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	tenantID, err := uuid.Parse(envelope.Payload.TenantID)
	if err != nil {
		c.log.Warn("order ready: invalid tenant_id", zap.String("raw", envelope.Payload.TenantID))
		_ = msg.Ack()
		return
	}

	orderID := envelope.Payload.OrderID
	if orderID == "" {
		_ = msg.Ack()
		return
	}

	// Create delivery task (idempotent — uses external_reference to deduplicate)
	externalRef := fmt.Sprintf("order:%s", orderID)
	t, err := c.taskSvc.CreateTaskFromOrder(ctx, tenantID, orderID, externalRef)
	if err != nil {
		c.log.Error("order ready: create task failed",
			zap.Error(err),
			zap.String("order_id", orderID),
		)
		_ = msg.Nak()
		return
	}

	c.log.Info("delivery task created from order",
		zap.String("task_id", t.ID.String()),
		zap.String("order_id", orderID),
	)

	// Auto-dispatch: find and assign nearest available rider (async, best-effort)
	if c.dispatcher != nil {
		go func() {
			dispatchCtx := context.Background()
			if dispatchErr := c.dispatcher.DispatchTask(dispatchCtx, tenantID, t.ID); dispatchErr != nil {
				c.log.Warn("auto-dispatch failed",
					zap.String("task_id", t.ID.String()),
					zap.Error(dispatchErr),
				)
			}
		}()
	}

	_ = msg.Ack()
}
