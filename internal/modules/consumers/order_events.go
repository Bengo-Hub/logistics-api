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

// orderReadyEvent represents the full event envelope from ordering.order.ready.
type orderReadyEvent struct {
	TenantID string `json:"tenantId"`
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
	ctx := context.Background()

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
