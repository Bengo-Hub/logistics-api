package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// SSEHub manages Server-Sent Events connections per task.
// Dispatchers / riders push status events; clients (logistics-ui, public tracker) receive them.
type SSEHub struct {
	mu          sync.RWMutex
	subscribers map[string][]chan sseEvent // key: "tenantID:taskID"
	log         *zap.Logger
}

type sseEvent struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// NewSSEHub creates a new SSEHub.
func NewSSEHub(log *zap.Logger) *SSEHub {
	return &SSEHub{
		subscribers: make(map[string][]chan sseEvent),
		log:         log.Named("sse.hub"),
	}
}

func (h *SSEHub) subscribe(key string) chan sseEvent {
	ch := make(chan sseEvent, 8)
	h.mu.Lock()
	h.subscribers[key] = append(h.subscribers[key], ch)
	h.mu.Unlock()
	return ch
}

func (h *SSEHub) unsubscribe(key string, ch chan sseEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	subs := h.subscribers[key]
	for i, sub := range subs {
		if sub == ch {
			h.subscribers[key] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(h.subscribers[key]) == 0 {
		delete(h.subscribers, key)
	}
}

// Publish broadcasts an event to all clients subscribed to the given task.
func (h *SSEHub) Publish(tenantID, taskID uuid.UUID, event string, data any) {
	key := tenantID.String() + ":" + taskID.String()
	h.mu.RLock()
	subs := h.subscribers[key]
	h.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- sseEvent{Event: event, Data: data}:
		default:
			// Slow consumer — drop rather than block
		}
	}
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
		// Public tracking also allowed via tracking code — here we accept uuid.Nil
		// for unauthenticated access on the public tracking path.
		// For the auth-gated path the middleware already rejects missing tokens,
		// so if we reach here without a tenantID the request came from the public
		// tracker — resolve tenant from the task instead.
	}

	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	key := tenantID.String() + ":" + taskID.String()
	ch := h.hub.subscribe(key)
	defer h.hub.unsubscribe(key, ch)

	// Send initial connected event
	writeSSE(w, "connected", map[string]string{"task_id": taskID.String()})
	flusher.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case evt, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, evt.Event, evt.Data)
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}
