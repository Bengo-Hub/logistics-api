package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"nhooyr.io/websocket"

	notifmod "github.com/bengobox/logistics-service/internal/modules/notifications"
)

// NotificationsHandler handles in-app operational-alert endpoints for logistics-ui's
// dispatcher bell.
type NotificationsHandler struct {
	log              *zap.Logger
	svc              *notifmod.Service
	hub              *notifmod.Hub
	wsOriginPatterns []string
}

// NewNotificationsHandler creates a new NotificationsHandler. originPatterns are the
// allowed WebSocket handshake Origins; an empty list falls back to "*" (dev convenience).
func NewNotificationsHandler(log *zap.Logger, svc *notifmod.Service, hub *notifmod.Hub, originPatterns []string) *NotificationsHandler {
	patterns := originPatterns
	if len(patterns) == 0 {
		patterns = []string{"*"}
	}
	return &NotificationsHandler{
		log:              log.Named("notifications.handler"),
		svc:              svc,
		hub:              hub,
		wsOriginPatterns: patterns,
	}
}

// List handles GET /api/v1/{tenant}/notifications?include_read=true
func (h *NotificationsHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	includeRead := r.URL.Query().Get("include_read") == "true"

	items, err := h.svc.List(r.Context(), tenantID, includeRead, 50)
	if err != nil {
		h.log.Error("list notifications", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"data": items, "total": len(items)})
}

// MarkRead handles PATCH /api/v1/{tenant}/notifications/{id}/read
func (h *NotificationsHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.svc.MarkRead(r.Context(), tenantID, id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "read"})
}

// MarkAllRead handles POST /api/v1/{tenant}/notifications/mark-all-read
func (h *NotificationsHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	n, err := h.svc.MarkAllRead(r.Context(), tenantID)
	if err != nil {
		h.log.Error("mark all read", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]int{"marked_read": n})
}

// StreamNotifications handles GET /api/v1/{tenant}/notifications/stream
// Upgrades to a WebSocket and pushes each new notification the moment it's created.
func (h *NotificationsHandler) StreamNotifications(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: h.wsOriginPatterns,
	})
	if err != nil {
		h.log.Warn("notifications ws: upgrade failed", zap.Error(err))
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	h.hub.ServeWS(r.Context(), conn, tenantID)
}
