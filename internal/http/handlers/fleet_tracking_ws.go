package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"github.com/bengobox/logistics-service/internal/platform/realtime"
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

// FleetTrackingHub fans out rider location_update broadcasts to every dispatcher connected
// to a tenant's live fleet map. A dispatcher's socket lands on whichever replica handled the
// upgrade while the rider's location POST can land on any replica, so the shared
// events.FanoutHub relays every update to all replicas and each delivers to its own sockets.
type FleetTrackingHub struct {
	fan *eventslib.FanoutHub
	log *zap.Logger
}

// NewFleetTrackingHub creates a new hub. relay may be nil, which degrades to single-pod
// delivery only (still correct when there is exactly one replica).
func NewFleetTrackingHub(log *zap.Logger, relay *eventslib.Broadcaster) *FleetTrackingHub {
	l := log.Named("fleet-tracking.hub")
	fan, err := eventslib.NewFanoutHub(relay, "fleet-tracking", 32)
	if err != nil {
		l.Warn("fleet-tracking.hub: relay subscribe failed, single-pod delivery only", zap.Error(err))
	}
	return &FleetTrackingHub{fan: fan, log: l}
}

// Broadcast delivers a rider's location update to every dispatcher watching this tenant's
// fleet map, on every replica.
func (h *FleetTrackingHub) Broadcast(tenantID, memberID uuid.UUID, lat, lng float64, heading, speed *float64) {
	b, err := json.Marshal(fleetWSMessage{
		Type: "location_update",
		Data: FleetLocationUpdate{
			RiderID:   memberID.String(),
			Latitude:  lat,
			Longitude: lng,
			Heading:   heading,
			Speed:     speed,
			Timestamp: time.Now().UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		return
	}
	h.fan.Publish(tenantID.String(), "", b)
}

// ServeWS registers conn for tenantID and blocks until it disconnects or ctx is cancelled.
func (h *FleetTrackingHub) ServeWS(ctx context.Context, conn *websocket.Conn, tenantID uuid.UUID) {
	sub := h.fan.Subscribe(tenantID.String())
	defer h.fan.Unsubscribe(sub)
	realtime.Pump(ctx, conn, sub, nil)
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
