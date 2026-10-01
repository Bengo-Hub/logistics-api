package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Bengo-Hub/httpware"
	eventslib "github.com/Bengo-Hub/shared-events"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SSEHub fans task status and ETA events out to SSE clients (logistics-ui, trackers).
//
// A client's stream lives on whichever replica the load balancer picked, while the status
// change is published by whichever replica handled the write, so events go through the shared
// events.FanoutHub, which relays them to every replica (each delivers to its own streams).
// Before, the hub was in-memory only: a tracker on pod B never saw a change made on pod A.
// Subscribers hold the scope "task:<id>" and only ever receive their own tenant's events.
type SSEHub struct {
	fan *eventslib.FanoutHub
	log *zap.Logger
}

type sseEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// NewSSEHub creates a new SSEHub. relay may be nil (single replica, local development).
func NewSSEHub(log *zap.Logger, relay *eventslib.Broadcaster) *SSEHub {
	l := log.Named("sse.hub")
	fan, err := eventslib.NewFanoutHub(relay, "task-sse", 8)
	if err != nil {
		l.Warn("sse relay subscribe failed, single-replica delivery only", zap.Error(err))
	}
	return &SSEHub{fan: fan, log: l}
}

func taskScope(taskID uuid.UUID) string { return "task:" + taskID.String() }

// Publish broadcasts an event to every client subscribed to the task, on every replica.
func (h *SSEHub) Publish(tenantID, taskID uuid.UUID, event string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	payload, err := json.Marshal(sseEvent{Event: event, Data: raw})
	if err != nil {
		return
	}
	h.fan.Publish(tenantID.String(), taskScope(taskID), payload)
}

// SSEHandler handles GET /api/v1/{tenant}/tasks/{taskId}/stream
type SSEHandler struct {
	hub *SSEHub
	log *zap.Logger
}

// NewSSEHandler creates a new SSEHandler.
func NewSSEHandler(hub *SSEHub, log *zap.Logger) *SSEHandler {
	return &SSEHandler{hub: hub, log: log.Named("sse.handler")}
}

// StreamTask handles GET /api/v1/{tenant}/tasks/{taskId}/stream
// Streams task status and ETA updates as Server-Sent Events.
func (h *SSEHandler) StreamTask(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		// The route is tenant- and auth-scoped; without a tenant there is nothing to stream.
		http.Error(w, "missing tenant", http.StatusUnauthorized)
		return
	}

	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}

	httpware.StreamHeaders(w)
	// Lift the server WriteTimeout for this long-lived response; the heartbeat keeps it alive.
	httpware.ExtendWriteDeadline(w)
	flusher := http.NewResponseController(w)

	sub := h.hub.fan.Subscribe(tenantID.String(), taskScope(taskID))
	defer h.hub.fan.Unsubscribe(sub)

	// Send initial connected event
	writeSSE(w, "connected", map[string]string{"task_id": taskID.String()})
	if err := flusher.Flush(); err != nil {
		return
	}

	// 15s: under the ingress and proxy idle timeouts.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			if flusher.Flush() != nil {
				return
			}
		case raw, ok := <-sub.C:
			if !ok {
				return
			}
			var evt sseEvent
			if json.Unmarshal(raw, &evt) != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Event, evt.Data)
			if flusher.Flush() != nil {
				return
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}
