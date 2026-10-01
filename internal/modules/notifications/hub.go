// Package notifications provides tenant-wide operational alerts for logistics-ui's
// dispatcher bell: a persisted feed (LogisticsNotification) plus a real-time WebSocket hub
// so a connected dispatcher sees a new alert immediately rather than on next poll.
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

// BroadcastNew delivers a newly-created notification to every dispatcher connected for the
// tenant, on every replica.
func (h *Hub) BroadcastNew(n *ent.LogisticsNotification) {
	b, err := json.Marshal(Message{Type: "notification", Data: n})
	if err != nil {
		return
	}
	h.fan.Publish(n.TenantID.String(), "", b)
}

// ServeWS registers conn for tenantID and blocks until it disconnects.
func (h *Hub) ServeWS(ctx context.Context, conn *websocket.Conn, tenantID uuid.UUID) {
	sub := h.fan.Subscribe(tenantID.String())
	defer h.fan.Unsubscribe(sub)
	hello, _ := json.Marshal(Message{Type: "ping"})
	eventslib.Pump(ctx, realtime.Socket(conn), sub, hello, nil)
}
