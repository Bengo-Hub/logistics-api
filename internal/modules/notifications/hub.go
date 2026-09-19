// Package notifications provides tenant-wide operational alerts for logistics-ui's
// dispatcher bell: a persisted feed (LogisticsNotification) plus a real-time WebSocket hub
// so a connected dispatcher sees a new alert immediately rather than on next poll.
package notifications

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/bengobox/logistics-service/internal/ent"
)

// Message is the envelope pushed to notification WebSocket clients.
type Message struct {
	Type string `json:"type"` // "notification" | "ping"
	Data any    `json:"data,omitempty"`
}

type client struct {
	tenantID uuid.UUID
	send     chan Message
}

const channelPrefix = "logistics:notif:"

// Hub manages active notification WebSocket connections, scoped per tenant (these are
// team-visible operational alerts, not personal messages — every dispatcher connected for
// a tenant sees the same feed).
//
// With several logistics-api replicas, a dispatcher's WebSocket lands on whichever pod
// handled its upgrade request, but the event that triggers a notification (an SLA monitor
// tick, an auto-dispatch failure) can happen on any replica. Broadcast delivers to this
// pod's own connected clients immediately and relays via Redis pub/sub so every other pod's
// hub does the same for its own — mirrors pos-api's notifications.Hub and this same
// service's own FleetTrackingHub, the same pattern already proven for this exact problem.
type Hub struct {
	mu       sync.RWMutex
	clients  map[*client]struct{}
	log      *zap.Logger
	redis    *redis.Client
	originID string
}

// NewHub creates a new notification hub. rdb may be nil, which degrades to single-pod
// delivery only.
func NewHub(log *zap.Logger, rdb *redis.Client) *Hub {
	return &Hub{
		clients:  make(map[*client]struct{}),
		log:      log.Named("notifications.hub"),
		redis:    rdb,
		originID: uuid.NewString(),
	}
}

// Start subscribes to the cross-pod relay channel; blocks until ctx is cancelled.
func (h *Hub) Start(ctx context.Context) {
	if h.redis == nil {
		h.log.Info("notifications.hub: no Redis client — single-pod broadcast only")
		return
	}
	sub := h.redis.PSubscribe(ctx, channelPrefix+"*")
	defer func() { _ = sub.Close() }()
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			h.relayFromRedis(msg.Channel, msg.Payload)
		}
	}
}

type relayEnvelope struct {
	Msg    Message `json:"msg"`
	Origin string  `json:"origin"`
}

func (h *Hub) relayFromRedis(channel, payload string) {
	idStr := strings.TrimPrefix(channel, channelPrefix)
	tenantID, err := uuid.Parse(idStr)
	if err != nil {
		return
	}
	var env relayEnvelope
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		h.log.Warn("notifications.hub: failed to decode redis relay message", zap.Error(err))
		return
	}
	if env.Origin == h.originID {
		return
	}
	h.sendLocal(tenantID, env.Msg)
}

// BroadcastNew delivers a newly-created notification to every dispatcher connected for the
// tenant — locally, and (via Redis) on every other replica too.
func (h *Hub) BroadcastNew(n *ent.LogisticsNotification) {
	msg := Message{Type: "notification", Data: n}
	h.sendLocal(n.TenantID, msg)
	h.publish(n.TenantID, msg)
}

func (h *Hub) sendLocal(tenantID uuid.UUID, msg Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.tenantID != tenantID {
			continue
		}
		select {
		case c.send <- msg:
		default:
			h.log.Warn("notifications.hub: send buffer full, dropping message",
				zap.Stringer("tenant_id", tenantID))
		}
	}
}

func (h *Hub) publish(tenantID uuid.UUID, msg Message) {
	if h.redis == nil {
		return
	}
	payload, err := json.Marshal(relayEnvelope{Msg: msg, Origin: h.originID})
	if err != nil {
		h.log.Warn("notifications.hub: failed to marshal redis relay payload", zap.Error(err))
		return
	}
	channel := channelPrefix + tenantID.String()
	if err := h.redis.Publish(context.Background(), channel, payload).Err(); err != nil {
		h.log.Warn("notifications.hub: redis publish failed", zap.Error(err), zap.String("channel", channel))
	}
}

// ServeWS registers conn as an active client for tenantID and blocks until it disconnects.
func (h *Hub) ServeWS(ctx context.Context, conn *websocket.Conn, tenantID uuid.UUID) {
	c := &client{tenantID: tenantID, send: make(chan Message, 32)}

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		close(c.send)
		h.mu.Unlock()
	}()

	go func() {
		for msg := range c.send {
			if err := wsjson.Write(ctx, conn, msg); err != nil {
				return
			}
		}
	}()

	c.send <- Message{Type: "ping"}

	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}
