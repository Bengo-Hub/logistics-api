package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"nhooyr.io/websocket"

	"github.com/bengobox/logistics-service/internal/ent"
	notifmod "github.com/bengobox/logistics-service/internal/modules/notifications"
)

// NotificationsHandler serves the dispatcher bell (/notifications, tenant-wide alerts) and the
// rider inbox (/riders/me/notifications, the signed-in rider's own messages).
type NotificationsHandler struct {
	log              *zap.Logger
	svc              *notifmod.Service
	hub              *notifmod.Hub
	client           *ent.Client
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

// SetClient wires the ent client used to resolve the signed-in rider for the inbox routes.
func (h *NotificationsHandler) SetClient(c *ent.Client) { h.client = c }

// audience resolves who the request reads for: nil (dispatcher feed) or the signed-in rider.
// It writes the error response itself and returns ok=false when it cannot.
func (h *NotificationsHandler) audience(w http.ResponseWriter, r *http.Request, rider bool) (uuid.UUID, *uuid.UUID, bool) {
	if !rider {
		tenantID := tenantIDFromClaims(r)
		if tenantID == uuid.Nil {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return uuid.Nil, nil, false
		}
		return tenantID, nil, true
	}
	if h.client == nil {
		http.Error(w, "rider inbox unavailable", http.StatusServiceUnavailable)
		return uuid.Nil, nil, false
	}
	tenantID, member, ok := currentRiderOf(w, r, h.client, h.log)
	if !ok {
		return uuid.Nil, nil, false
	}
	id := member.ID
	return tenantID, &id, true
}

func (h *NotificationsHandler) list(w http.ResponseWriter, r *http.Request, rider bool) {
	tenantID, member, ok := h.audience(w, r, rider)
	if !ok {
		return
	}
	includeRead := r.URL.Query().Get("include_read") == "true"
	items, err := h.svc.List(r.Context(), tenantID, member, includeRead, 50)
	if err != nil {
		h.log.Error("list notifications", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	unread, err := h.svc.UnreadCount(r.Context(), tenantID, member)
	if err != nil {
		h.log.Warn("count unread notifications", zap.Error(err))
	}
	respondJSON(w, http.StatusOK, map[string]any{"data": items, "total": len(items), "unread": unread})
}

func (h *NotificationsHandler) markRead(w http.ResponseWriter, r *http.Request, rider bool) {
	tenantID, member, ok := h.audience(w, r, rider)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := h.svc.MarkRead(r.Context(), tenantID, member, id); err != nil {
		http.Error(w, "notification not found", http.StatusNotFound)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "read"})
}

func (h *NotificationsHandler) markAllRead(w http.ResponseWriter, r *http.Request, rider bool) {
	tenantID, member, ok := h.audience(w, r, rider)
	if !ok {
		return
	}
	n, err := h.svc.MarkAllRead(r.Context(), tenantID, member)
	if err != nil {
		h.log.Error("mark all read", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]int{"marked_read": n})
}

func (h *NotificationsHandler) stream(w http.ResponseWriter, r *http.Request, rider bool) {
	tenantID, member, ok := h.audience(w, r, rider)
	if !ok {
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.wsOriginPatterns})
	if err != nil {
		h.log.Warn("notifications ws: upgrade failed", zap.Error(err))
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	if member != nil {
		h.hub.ServeRiderWS(r.Context(), conn, tenantID, *member)
		return
	}
	h.hub.ServeWS(r.Context(), conn, tenantID)
}

// List handles GET /api/v1/{tenant}/notifications?include_read=true (dispatcher alerts).
func (h *NotificationsHandler) List(w http.ResponseWriter, r *http.Request) { h.list(w, r, false) }

// MarkRead handles PATCH /api/v1/{tenant}/notifications/{id}/read
func (h *NotificationsHandler) MarkRead(w http.ResponseWriter, r *http.Request) {
	h.markRead(w, r, false)
}

// MarkAllRead handles POST /api/v1/{tenant}/notifications/mark-all-read
func (h *NotificationsHandler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	h.markAllRead(w, r, false)
}

// StreamNotifications handles GET /api/v1/{tenant}/notifications/stream (WebSocket): each new
// dispatcher alert the moment it is created.
func (h *NotificationsHandler) StreamNotifications(w http.ResponseWriter, r *http.Request) {
	h.stream(w, r, false)
}

// ListMine handles GET /api/v1/{tenant}/riders/me/notifications?include_read=true (rider inbox).
func (h *NotificationsHandler) ListMine(w http.ResponseWriter, r *http.Request) { h.list(w, r, true) }

// MarkMineRead handles PATCH /api/v1/{tenant}/riders/me/notifications/{id}/read
func (h *NotificationsHandler) MarkMineRead(w http.ResponseWriter, r *http.Request) {
	h.markRead(w, r, true)
}

// MarkAllMineRead handles POST /api/v1/{tenant}/riders/me/notifications/mark-all-read
func (h *NotificationsHandler) MarkAllMineRead(w http.ResponseWriter, r *http.Request) {
	h.markAllRead(w, r, true)
}

// StreamMine handles GET /api/v1/{tenant}/riders/me/notifications/stream (WebSocket): the
// rider's own new messages.
func (h *NotificationsHandler) StreamMine(w http.ResponseWriter, r *http.Request) {
	h.stream(w, r, true)
}
