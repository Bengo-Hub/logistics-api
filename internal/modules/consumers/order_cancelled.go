package consumers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

const orderCancelledConsumer = "logistics-service-order-cancelled"

// OrderCancelledConsumer closes the delivery task when ordering cancels an order. Before it
// existed, a customer or outlet cancelling an order that already had a task left the task open:
// the rider kept riding to the outlet, auto-dispatch kept offering the job, and ordering's own
// cancel call went to a logistics route that never existed.
type OrderCancelledConsumer struct {
	log     *zap.Logger
	taskSvc *tasks.Service
	svcCtx  context.Context //nolint:containedctx
}

// NewOrderCancelledConsumer creates the consumer.
func NewOrderCancelledConsumer(log *zap.Logger, taskSvc *tasks.Service) *OrderCancelledConsumer {
	return &OrderCancelledConsumer{log: log.Named("consumers.order_cancelled"), taskSvc: taskSvc, svcCtx: context.Background()}
}

// Start consumes ordering.order.cancelled until ctx ends.
func (c *OrderCancelledConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	c.svcCtx = ctx
	eventslib.SubscribeQueueWithRebind(
		c.log, js, "ordering", "ordering.order.cancelled", orderCancelledConsumer, c.handleMessage,
		nats.Durable(orderCancelledConsumer),
		nats.AckExplicit(),
		nats.AckWait(30*time.Second),
		nats.MaxDeliver(5),
		nats.DeliverNew(),
	)
	c.log.Info("order cancelled consumer started", zap.String("durable", orderCancelledConsumer))
	<-ctx.Done()
	return nil
}

// cancelledOrderRef returns the tenant, task reference and reason carried by an
// ordering.order.cancelled event.
func cancelledOrderRef(evt *eventslib.Event) (uuid.UUID, string, string, error) {
	if evt == nil || evt.TenantID == uuid.Nil {
		return uuid.Nil, "", "", fmt.Errorf("missing tenant")
	}
	orderID, _ := evt.Payload["order_id"].(string)
	if _, err := uuid.Parse(orderID); err != nil {
		return uuid.Nil, "", "", fmt.Errorf("invalid order_id %q", orderID)
	}
	reason, _ := evt.Payload["reason"].(string)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "order cancelled"
	} else {
		reason = "order cancelled: " + reason
	}
	return evt.TenantID, "order:" + orderID, reason, nil
}

func (c *OrderCancelledConsumer) handleMessage(msg *nats.Msg) {
	evt, err := eventslib.FromJSON(msg.Data)
	if err != nil {
		c.log.Warn("order cancelled: bad envelope", zap.Error(err))
		_ = msg.Ack()
		return
	}
	tenantID, ref, reason, err := cancelledOrderRef(evt)
	if err != nil {
		_ = msg.Ack()
		return
	}
	ctx, cancel := context.WithTimeout(c.svcCtx, 20*time.Second)
	defer cancel()
	t, err := c.taskSvc.FindTaskByReference(ctx, tenantID, ref)
	if err != nil {
		_ = msg.Nak()
		return
	}
	if t == nil || tasks.IsTerminal(t.Status) {
		_ = msg.Ack() // never dispatched (pickup order, not ready yet) or already closed
		return
	}
	if _, err := c.taskSvc.CancelTask(ctx, tenantID, t.ID, reason, tasks.Actor{Type: "ordering"}); err != nil &&
		!errors.Is(err, tasks.ErrTaskClosed) {
		c.log.Warn("order cancelled: could not cancel task", zap.String("task_id", t.ID.String()), zap.Error(err))
		_ = msg.Nak()
		return
	}
	c.log.Info("delivery task cancelled with its order", zap.String("task_id", t.ID.String()), zap.String("ref", ref))
	_ = msg.Ack()
}
