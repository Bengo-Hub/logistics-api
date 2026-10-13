// Package notifications provides persisted notifications (LogisticsNotification) plus a
// real-time WebSocket hub for two audiences: tenant-wide operational alerts on logistics-ui's
// dispatcher bell, and each rider's own inbox in the rider app.
package notifications

import (
	"context"
	"encoding/json"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/platform/realtime"
)

// Message is the envelope pushed to notification WebSocket clients.
type Message struct {
	Type string `json:"type"` // "notification" | "ping"
	Data any    `json:"data,omitempty"`
}

// Hub manages notification WebSocket connections, scoped per tenant (team-visible operational
// alerts, so every dispatcher connected for a tenant sees the same feed).
//
// A dispatcher's socket lands on whichever replica handled the upgrade, while the event that
// raises a notification (SLA monitor tick, auto-dispatch failure) can happen on any replica.
// The shared events.FanoutHub relays every message to all replicas and each delivers to its
// own sockets; this type only owns the WebSocket write loop.
type Hub struct {
	fan *eventslib.FanoutHub
	log *zap.Logger
}

// NewHub creates a notification hub. relay may be nil, which degrades to single-pod delivery.
func NewHub(log *zap.Logger, relay *eventslib.Broadcaster) *Hub {
	l := log.Named("notifications.hub")
	fan, err := eventslib.NewFanoutHub(relay, "dispatcher-notifications", 32)
	if err != nil {
		l.Warn("notifications.hub: relay subscribe failed, single-pod delivery only", zap.Error(err))
	}
	return &Hub{fan: fan, log: l}
}

// riderKey is the fan-out key of one rider's inbox; dispatchers subscribe to the bare tenant id,
// so a rider's message never reaches the dispatcher bell and the reverse.
func riderKey(tenantID, memberID uuid.UUID) string {
	return tenantID.String() + ":rider:" + memberID.String()
}

// BroadcastNew delivers a newly-created notification to its audience on every replica: the
// rider it is for, or every dispatcher connected for the tenant.
func (h *Hub) BroadcastNew(n *ent.LogisticsNotification) {
	b, err := json.Marshal(Message{Type: "notification", Data: n})
	if err != nil {
		return
	}
	key := n.TenantID.String()
	if n.RecipientMemberID != nil {
		key = riderKey(n.TenantID, *n.RecipientMemberID)
	}
	h.fan.Publish(key, "", b)
}

// ServeWS registers a dispatcher's conn for tenantID and blocks until it disconnects.
func (h *Hub) ServeWS(ctx context.Context, conn *websocket.Conn, tenantID uuid.UUID) {
	h.serve(ctx, conn, tenantID.String())
}

// ServeRiderWS registers a rider's conn for their own inbox and blocks until it disconnects.
func (h *Hub) ServeRiderWS(ctx context.Context, conn *websocket.Conn, tenantID, memberID uuid.UUID) {
	h.serve(ctx, conn, riderKey(tenantID, memberID))
}

func (h *Hub) serve(ctx context.Context, conn *websocket.Conn, key string) {
	sub := h.fan.Subscribe(key)
	defer h.fan.Unsubscribe(sub)
	hello, _ := json.Marshal(Message{Type: "ping"})
	eventslib.Pump(ctx, realtime.Socket(conn), sub, hello, nil)
}
