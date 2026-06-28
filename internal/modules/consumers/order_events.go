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
	orderReadyConsumer    = "logistics-service-order-ready"
	orderReadyAckWait     = 30 * time.Second
	orderReadyMaxDeliver  = 5
)

// OrderReadyConsumer subscribes to ordering.order.ready and creates delivery tasks.
type OrderReadyConsumer struct {
	log        *zap.Logger
	taskSvc    *tasks.Service
	dispatcher *dispatch.AutoDispatcher
	// svcCtx is the service-level context stored during Start() so that
	// the NATS handler (which has no context parameter) can derive bounded
	// goroutine contexts that respect service shutdown.
	svcCtx context.Context //nolint:containedctx
	// hasFeature gates ordering→logistics delivery sync by subscription entitlement.
	// Nil → fail open.
	hasFeature func(ctx context.Context, tenantID, feature string) bool
}

// NewOrderReadyConsumer creates the consumer.
func NewOrderReadyConsumer(log *zap.Logger, taskSvc *tasks.Service, dispatcher *dispatch.AutoDispatcher) *OrderReadyConsumer {
	return &OrderReadyConsumer{
		log:        log.Named("consumers.order_ready"),
		taskSvc:    taskSvc,
		dispatcher: dispatcher,
		svcCtx:     context.Background(), // replaced by Start()
	}
}

// SetFeatureGate wires the subscription entitlement check used to gate delivery-task sync.
func (c *OrderReadyConsumer) SetFeatureGate(fn func(ctx context.Context, tenantID, feature string) bool) {
	c.hasFeature = fn
}

// entitled reports whether the tenant may have a delivery task synced. Fails open when no
// gate is wired or tenantID is empty (never strand a legitimate delivery on a parse miss).
func (c *OrderReadyConsumer) entitled(ctx context.Context, tenantID, feature string) bool {
	if c.hasFeature == nil || tenantID == "" {
		return true
	}
	return c.hasFeature(ctx, tenantID, feature)
}

// Start begins consuming ordering.order.ready via JetStream.
func (c *OrderReadyConsumer) Start(ctx context.Context, js nats.JetStreamContext) error {
	c.svcCtx = ctx
	eventslib.SubscribeQueueWithRebind(
		c.log,
		js,
		"ordering",
		"ordering.order.ready",
		orderReadyConsumer,
		c.handleMessage,
		nats.Durable(orderReadyConsumer),
		nats.AckExplicit(),
		nats.AckWait(orderReadyAckWait),
		nats.MaxDeliver(orderReadyMaxDeliver),
		nats.DeliverAll(),
	)
	c.log.Info("order ready consumer started", zap.String("durable", orderReadyConsumer))

	<-ctx.Done()
	return nil
}

// orderReadyEvent represents the full event envelope from ordering.order.ready.
// ordering-backend now publishes the fleet-uniform shared-events envelope
// (tenant_id/payload), so the wrapper is decoded with those field names.
type orderReadyEvent struct {
	TenantID string `json:"tenant_id"`
	Data     struct {
		TenantID        string                 `json:"tenant_id"`
		OrderID         string                 `json:"order_id"`
		OrderNumber     string                 `json:"order_number"`
		OutletID        string                 `json:"outlet_id"`
		CustomerID      string                 `json:"customer_id"`
		PaymentMethod   string                 `json:"payment_method"`
		CashOnDelivery  float64                `json:"cash_on_delivery"`
		DeliveryFee     float64                `json:"delivery_fee"`
		GrandTotal      float64                `json:"grand_total"`
		CustomerName    string                 `json:"customer_name"`
		CustomerPhone   string                 `json:"customer_phone"`
		Instructions    string                 `json:"instructions"`
		FulfillmentType string                 `json:"fulfillment_type"`
		OutletLocation  map[string]interface{} `json:"outlet_location"`
		DeliveryAddress map[string]interface{} `json:"delivery_address"`
	} `json:"data"`
}

func (c *OrderReadyConsumer) handleMessage(msg *nats.Msg) {
	ctx := c.svcCtx

	var envelope orderReadyEvent
	if err := json.Unmarshal(msg.Data, &envelope); err != nil {
		c.log.Warn("order ready: unmarshal failed", zap.Error(err))
		_ = msg.Nak()
		return
	}

	// Resolve tenant ID (check both top-level and nested data)
	rawTenantID := envelope.Data.TenantID
	if rawTenantID == "" {
		rawTenantID = envelope.TenantID
	}
	tenantID, err := uuid.Parse(rawTenantID)
	if err != nil {
		c.log.Warn("order ready: invalid tenant_id", zap.String("raw", rawTenantID))
		_ = msg.Ack()
		return
	}

	orderID := envelope.Data.OrderID
	if orderID == "" {
		_ = msg.Ack()
		return
	}

	// Gate the cross-service delivery sync on the core logistics entitlement. Fails open on a
	// subscriptions-api outage so a downtime never strands a legitimate delivery. basic_logistics_access
	// is auto-injected into ordering plans, so any tenant whose orders reach here is normally entitled.
	if !c.entitled(ctx, rawTenantID, "basic_logistics_access") {
		c.log.Debug("order ready: tenant lacks basic_logistics_access — skipping delivery task sync",
			zap.String("tenant_id", rawTenantID), zap.String("order_id", orderID))
		_ = msg.Ack()
		return
	}

	// Build task creation request with full delivery context
	req := tasks.CreateTaskFromOrderRequest{
		OrderID:         orderID,
		OrderNumber:     envelope.Data.OrderNumber,
		CustomerName:    envelope.Data.CustomerName,
		CustomerPhone:   envelope.Data.CustomerPhone,
		Instructions:    envelope.Data.Instructions,
		CashOnDelivery:  envelope.Data.CashOnDelivery,
		FulfillmentType: envelope.Data.FulfillmentType,
	}

	// Extract pickup location from outlet
	if loc := envelope.Data.OutletLocation; loc != nil {
		req.PickupName, _ = loc["name"].(string)
		req.PickupLat, _ = toFloat64(loc["latitude"])
		req.PickupLng, _ = toFloat64(loc["longitude"])
	}

	// Extract dropoff location from delivery address
	if addr := envelope.Data.DeliveryAddress; addr != nil {
		req.DropoffLat, _ = toFloat64(addr["latitude"])
		req.DropoffLng, _ = toFloat64(addr["longitude"])
		req.DropoffName = buildAddressLabel(addr)
		if phone, ok := addr["contact_phone"].(string); ok && phone != "" {
			req.CustomerPhone = phone
		}
		if name, ok := addr["contact_name"].(string); ok && name != "" {
			req.CustomerName = name
		}
	}

	// Create delivery task (idempotent — uses external_reference to deduplicate)
	externalRef := fmt.Sprintf("order:%s", orderID)
	t, err := c.taskSvc.CreateTaskFromOrder(ctx, tenantID, externalRef, req)
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

	// Auto-dispatch: find and assign nearest available rider (async, best-effort).
	// Derive from svcCtx (not Background) so the goroutine is cancelled when the
	// service shuts down — prevents double-dispatch on rolling restarts.
	if c.dispatcher != nil {
		dispatchCtx, dispatchCancel := context.WithTimeout(c.svcCtx, 30*time.Second)
		go func() {
			defer dispatchCancel()
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

// toFloat64 safely converts an interface{} to float64.
func toFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case json.Number:
		f, err := val.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// buildAddressLabel constructs a human-readable address from the address map.
func buildAddressLabel(addr map[string]interface{}) string {
	parts := []string{}
	for _, key := range []string{"address_line1", "city", "county"} {
		if v, ok := addr[key].(string); ok && v != "" {
			parts = append(parts, v)
		}
	}
	result := ""
	for i, p := range parts {
		if i > 0 {
			result += ", "
		}
		result += p
	}
	return result
}
