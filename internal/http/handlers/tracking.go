package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

// TrackingHandler handles public tracking endpoints (no auth required).
type TrackingHandler struct {
	taskSvc *tasks.Service
	log     *zap.Logger
}

// NewTrackingHandler creates a new tracking handler.
func NewTrackingHandler(taskSvc *tasks.Service, log *zap.Logger) *TrackingHandler {
	return &TrackingHandler{
		taskSvc: taskSvc,
		log:     log.Named("handlers.tracking"),
	}
}

// TrackByCode handles GET /api/v1/track/{trackingCode} (public, no auth).
func (h *TrackingHandler) TrackByCode(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "trackingCode")
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tracking code required"})
		return
	}

	t, err := h.taskSvc.GetTaskByTrackingCode(r.Context(), code)
	if err != nil {
		h.log.Debug("tracking lookup failed", zap.String("code", code), zap.Error(err))
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "tracking code not found"})
		return
	}

	// Build status history from task events
	var statusHistory []map[string]any
	if t.Edges.Events != nil {
		for _, ev := range t.Edges.Events {
			statusHistory = append(statusHistory, map[string]any{
				"status": ev.EventType,
				"at":     ev.OccurredAt,
				"label":  statusLabel(ev.EventType),
			})
		}
	}

	// Build rider info from latest assignment
	var riderInfo map[string]any
	if t.Edges.Assignments != nil && len(t.Edges.Assignments) > 0 {
		assignment := t.Edges.Assignments[0]
		riderInfo = map[string]any{
			"id":     assignment.FleetMemberID.String(),
			"status": assignment.Status,
		}
	}

	// Build step locations from address_json
	var pickupName, dropoffName string
	var pickupAddrJSON, dropoffAddrJSON map[string]any
	if t.Edges.Steps != nil {
		for _, step := range t.Edges.Steps {
			if step.StepType == "pickup" {
				pickupName = step.LocationName
				pickupAddrJSON = step.AddressJSON
			} else if step.StepType == "dropoff" {
				dropoffName = step.LocationName
				dropoffAddrJSON = step.AddressJSON
			}
		}
	}

	// Determine if live tracking is available (task is in transit)
	liveTracking := t.Status == "en_route" || t.Status == "accepted" ||
		t.Status == "en_route_pickup" || t.Status == "en_route_dropoff" ||
		t.Status == "arrived_pickup" || t.Status == "picked_up"

	resp := map[string]any{
		"tracking_code":          t.TrackingCode,
		"status":                 t.Status,
		"task_type":              t.TaskType,
		"status_history":         statusHistory,
		"rider":                  riderInfo,
		"pickup_location":        pickupName,
		"pickup_address":         pickupAddrJSON,
		"dropoff_location":       dropoffName,
		"dropoff_address":        dropoffAddrJSON,
		"live_tracking_available": liveTracking,
		"created_at":             t.CreatedAt,
		"updated_at":             t.UpdatedAt,
	}

	writeJSON(w, http.StatusOK, resp)
}

func statusLabel(status string) string {
	labels := map[string]string{
		"pending":          "Task Created",
		"assigned":         "Rider Assigned",
		"accepted":         "Rider Accepted",
		"en_route":         "On the Way",
		"en_route_pickup":  "Heading to Pickup",
		"arrived_pickup":   "Arrived at Pickup",
		"picked_up":        "Order Picked Up",
		"en_route_dropoff": "On the Way to You",
		"arrived_dropoff":  "Arrived at Destination",
		"delivered":        "Delivered",
		"completed":        "Completed",
		"failed":           "Delivery Failed",
		"cancelled":        "Cancelled",
	}
	if l, ok := labels[status]; ok {
		return l
	}
	return status
}
