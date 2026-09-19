package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// FleetLocationUpdate is one rider's position, shaped to match the "data" field of
// @bengo-hub/maps' LiveFleetMap WebSocket message contract exactly (its compiled source
// reads msg.data.{rider_id,latitude,longitude,heading,speed,timestamp}).
type FleetLocationUpdate struct {
	RiderID   string   `json:"rider_id"`
	Latitude  float64  `json:"latitude"`
	Longitude float64  `json:"longitude"`
	Heading   *float64 `json:"heading,omitempty"`
	Speed     *float64 `json:"speed,omitempty"`
	Timestamp string   `json:"timestamp"`
}

type fleetWSMessage struct {
	Type string              `json:"type"`
	Data FleetLocationUpdate `json:"data"`
}

const fleetTrackingChannelPrefix = "logistics:tracking:fleet:"

// fleetWSClient is a single connected dispatcher's WebSocket session.
type fleetWSClient struct {
	tenantID uuid.UUID
	send     chan fleetWSMessage
}

// FleetTrackingHub fans out rider location_update broadcasts to every dispatcher connected
// to a tenant's live fleet map.
//
// With several logistics-api replicas, a dispatcher's WebSocket lands on whichever pod
// handled its upgrade request, but the rider location POST that triggers a broadcast can
// land on any replica. Without a cross-pod relay, a broadcast only reaches the (on average
// 1-in-N) dispatchers who happen to share the broadcasting pod. Broadcast delivers to this
// pod's own local clients immediately AND relays via Redis pub/sub so every other pod's hub
// does the same for its own — mirrors pos-api's notifications.Hub, the same pattern already
// proven on this platform for this exact cross-pod-WebSocket problem.
type FleetTrackingHub struct {
	mu       sync.RWMutex
	clients  map[*fleetWSClient]struct{}
	log      *zap.Logger
	redis    *redis.Client
	originID string
}

// NewFleetTrackingHub creates a new hub. rdb may be nil, which degrades to single-pod
// delivery only (still correct when there is exactly one replica).
func NewFleetTrackingHub(log *zap.Logger, rdb *redis.Client) *FleetTrackingHub {
	return &FleetTrackingHub{
		clients:  make(map[*fleetWSClient]struct{}),
		log:      log.Named("fleet-tracking.hub"),
		redis:    rdb,
		originID: uuid.NewString(),
	}
}

// Start subscribes to the cross-pod relay channel and relays messages to this pod's local
// clients. Blocks until ctx is cancelled — run in a goroutine.
func (h *FleetTrackingHub) Start(ctx context.Context) {
	if h.redis == nil {
		h.log.Info("fleet-tracking.hub: no Redis client — single-pod broadcast only")
		return
	}
	sub := h.redis.PSubscribe(ctx, fleetTrackingChannelPrefix+"*")
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

type fleetRelayEnvelope struct {
	Msg    fleetWSMessage `json:"msg"`
	Origin string         `json:"origin"`
}

// relayFromRedis decodes a cross-pod relay message and delivers it to this pod's local
// clients. Skips messages this same pod originally published — Broadcast already delivers
// to local clients synchronously before publishing, so relaying our own publish back would
// double-deliver it.
func (h *FleetTrackingHub) relayFromRedis(channel, payload string) {
	idStr := strings.TrimPrefix(channel, fleetTrackingChannelPrefix)
	tenantID, err := uuid.Parse(idStr)
	if err != nil {
		return
	}
	var env fleetRelayEnvelope
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		h.log.Warn("fleet-tracking.hub: failed to decode redis relay message", zap.Error(err))
		return
	}
	if env.Origin == h.originID {
		return
	}
	h.sendLocal(tenantID, env.Msg)
}

// Broadcast delivers a rider's location update to every dispatcher watching this tenant's
// fleet map — locally, and (via Redis) on every other replica too.
func (h *FleetTrackingHub) Broadcast(tenantID, memberID uuid.UUID, lat, lng float64, heading, speed *float64) {
	msg := fleetWSMessage{
		Type: "location_update",
		Data: FleetLocationUpdate{
			RiderID:   memberID.String(),
			Latitude:  lat,
			Longitude: lng,
			Heading:   heading,
			Speed:     speed,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	}
	h.sendLocal(tenantID, msg)
	h.publish(tenantID, msg)
}

func (h *FleetTrackingHub) sendLocal(tenantID uuid.UUID, msg fleetWSMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if c.tenantID != tenantID {
			continue
		}
		select {
		case c.send <- msg:
		default:
			h.log.Warn("fleet-tracking.hub: send buffer full, dropping update",
				zap.Stringer("tenant_id", tenantID))
		}
	}
}

// publish relays msg to every other pod via Redis. No-op when Redis is not configured.
func (h *FleetTrackingHub) publish(tenantID uuid.UUID, msg fleetWSMessage) {
	if h.redis == nil {
		return
	}
	payload, err := json.Marshal(fleetRelayEnvelope{Msg: msg, Origin: h.originID})
	if err != nil {
		h.log.Warn("fleet-tracking.hub: failed to marshal redis relay payload", zap.Error(err))
		return
	}
	channel := fleetTrackingChannelPrefix + tenantID.String()
	if err := h.redis.Publish(context.Background(), channel, payload).Err(); err != nil {
		h.log.Warn("fleet-tracking.hub: redis publish failed", zap.Error(err), zap.String("channel", channel))
	}
}

// ServeWS registers conn as an active client for tenantID and blocks until it disconnects
// or ctx is cancelled.
func (h *FleetTrackingHub) ServeWS(ctx context.Context, conn *websocket.Conn, tenantID uuid.UUID) {
	c := &fleetWSClient{tenantID: tenantID, send: make(chan fleetWSMessage, 32)}

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

	// Reader loop — this hub is push-only; incoming frames are just discarded, but reading
	// is required to detect the client closing the connection.
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
	}
}

// FleetTrackingWSHandler handles the fleet-wide live tracking WebSocket upgrade.
type FleetTrackingWSHandler struct {
	hub              *FleetTrackingHub
	log              *zap.Logger
	wsOriginPatterns []string
}

// NewFleetTrackingWSHandler creates a new FleetTrackingWSHandler. originPatterns are the
// allowed WebSocket handshake Origins (glob patterns) — pass the same list used for the
// service's regular CORS config; an empty list falls back to "*" (dev convenience).
func NewFleetTrackingWSHandler(log *zap.Logger, hub *FleetTrackingHub, originPatterns []string) *FleetTrackingWSHandler {
	patterns := originPatterns
	if len(patterns) == 0 {
		patterns = []string{"*"}
	}
	return &FleetTrackingWSHandler{
		hub:              hub,
		log:              log.Named("fleet-tracking.ws"),
		wsOriginPatterns: patterns,
	}
}

// ServeFleetWS handles GET /api/v1/{tenant}/tracking/fleet/ws
// Upgrades to a WebSocket and streams real-time rider location_update events for the
// tenant's whole fleet — the live-push counterpart to GET /tracking/fleet's REST snapshot.
func (h *FleetTrackingWSHandler) ServeFleetWS(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: h.wsOriginPatterns,
	})
	if err != nil {
		h.log.Warn("fleet ws: upgrade failed", zap.Error(err))
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	h.hub.ServeWS(r.Context(), conn, tenantID)
}
