package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	"github.com/bengobox/logistics-service/internal/ent/telemetrypoint"
	"github.com/bengobox/logistics-service/internal/ent/telemetrystream"
	telemetrysvc "github.com/bengobox/logistics-service/internal/modules/telemetry"
)

// terminalTaskStatuses mirrors the deny-list used by the task service's own completion
// guard (internal/modules/tasks/service.go) — kept in sync manually since there's no
// shared status-classification helper yet.
var terminalTaskStatuses = []string{"delivered", "completed", "cancelled", "failed"}

// TelemetryHandler handles GPS telemetry ingestion and stream query endpoints.
type TelemetryHandler struct {
	log      *zap.Logger
	svc      *telemetrysvc.Service
	client   *ent.Client
	fleetHub *FleetTrackingHub
}

// NewTelemetryHandler creates a new TelemetryHandler.
func NewTelemetryHandler(log *zap.Logger, svc *telemetrysvc.Service, client *ent.Client) *TelemetryHandler {
	return &TelemetryHandler{
		log:    log.Named("telemetry.handler"),
		svc:    svc,
		client: client,
	}
}

// SetFleetHub wires the fleet-tracking WebSocket hub so IngestLocation can broadcast each
// GPS update in real time, in addition to the persistence/GEO-index work it already does.
func (h *TelemetryHandler) SetFleetHub(hub *FleetTrackingHub) { h.fleetHub = hub }

// RegisterRoutes registers telemetry routes on the given tenant-scoped router.
func (h *TelemetryHandler) RegisterRoutes(r chi.Router) {
	r.Route("/telemetry", func(t chi.Router) {
		t.Get("/", h.GetSummary) // GET /telemetry?period=today — summary for tracking/dashboard
		t.Post("/location", h.IngestLocation)
		t.Post("/stream/end", h.EndStream)
		t.Get("/streams", h.ListStreams)
		t.Get("/streams/{streamId}/points", h.ListPoints)
	})
}

// RegisterFleetTrackingRoute registers GET /tracking/fleet on the given router. Kept
// separate from RegisterRoutes so it can be mounted inside the existing /tracking group
// (router.go), which already carries the live_tracking subscription + rate-limit gates.
func (h *TelemetryHandler) RegisterFleetTrackingRoute(r chi.Router) {
	r.Get("/fleet", h.GetFleetTracking)
}

// TelemetrySummary is returned by GET /telemetry.
type TelemetrySummary struct {
	Period                 string  `json:"period"`
	ActiveRiders           int     `json:"active_riders"`
	CompletedTasks         int     `json:"completed_tasks"`
	AvgDeliveryTimeMinutes float64 `json:"avg_delivery_time_minutes"`
	ActiveStreams          int     `json:"active_streams"`
}

// GetSummary handles GET /api/v1/{tenant}/telemetry?period=today
// Returns a live-status summary used by the tracking page dashboard.
func (h *TelemetryHandler) GetSummary(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	period := r.URL.Query().Get("period")
	if period == "" {
		period = "today"
	}

	var since time.Time
	switch period {
	case "today":
		now := time.Now()
		since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	case "7d":
		since = time.Now().AddDate(0, 0, -7)
	case "30d":
		since = time.Now().AddDate(0, 0, -30)
	default:
		now := time.Now()
		since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		period = "today"
	}

	ctx := r.Context()

	// Active streams (riders currently online)
	activeStreams, _ := h.client.TelemetryStream.Query().
		Where(
			telemetrystream.TenantID(tenantID),
			telemetrystream.Status("active"),
		).
		Count(ctx)

	// Active riders = fleet members with an active stream
	activeRiders, _ := h.client.FleetMember.Query().
		Where(
			fleetmember.TenantID(tenantID),
			fleetmember.StatusEQ("active"),
		).
		Count(ctx)

	// Completed tasks in the period — use UpdatedAt as proxy for completion time
	completedTasks, _ := h.client.Task.Query().
		Where(
			task.TenantID(tenantID),
			task.StatusEQ("completed"),
			task.UpdatedAtGTE(since),
		).
		Count(ctx)

	// Average delivery time (created_at → updated_at) for tasks completed in period
	var avgMins float64
	completed, err := h.client.Task.Query().
		Where(
			task.TenantID(tenantID),
			task.StatusEQ("completed"),
			task.UpdatedAtGTE(since),
		).
		All(ctx)
	if err == nil && len(completed) > 0 {
		var totalMins float64
		var count int
		for _, t := range completed {
			diff := t.UpdatedAt.Sub(t.CreatedAt).Minutes()
			if diff > 0 {
				totalMins += diff
				count++
			}
		}
		if count > 0 {
			avgMins = totalMins / float64(count)
		}
	}

	respondJSON(w, http.StatusOK, TelemetrySummary{
		Period:                 period,
		ActiveRiders:           activeRiders,
		CompletedTasks:         completedTasks,
		AvgDeliveryTimeMinutes: avgMins,
		ActiveStreams:          activeStreams,
	})
}

// IngestLocation handles POST /api/v1/{tenant}/telemetry/location
// Called by the rider app on every GPS update (typically every 5–15 seconds).
func (h *TelemetryHandler) IngestLocation(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	memberID, err := h.resolveFleetMember(r, tenantID)
	if err != nil || memberID == uuid.Nil {
		http.Error(w, "rider not found in fleet", http.StatusForbidden)
		return
	}

	var pt telemetrysvc.LocationPoint
	if err := json.NewDecoder(r.Body).Decode(&pt); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.svc.IngestLocation(r.Context(), tenantID, memberID, pt); err != nil {
		h.log.Error("ingest location", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	if h.fleetHub != nil {
		h.fleetHub.Broadcast(tenantID, memberID, pt.Latitude, pt.Longitude, pt.BearingDeg, pt.SpeedKph)
	}

	w.WriteHeader(http.StatusNoContent)
}

// EndStream handles POST /api/v1/{tenant}/telemetry/stream/end
// Called when the rider goes offline or ends their shift.
func (h *TelemetryHandler) EndStream(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	memberID, err := h.resolveFleetMember(r, tenantID)
	if err != nil || memberID == uuid.Nil {
		http.Error(w, "rider not found in fleet", http.StatusForbidden)
		return
	}

	if err := h.svc.EndStream(r.Context(), tenantID, memberID); err != nil {
		h.log.Error("end stream", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "ended"})
}

// ListStreams handles GET /api/v1/{tenant}/telemetry/streams
// Query params: member_id, status
func (h *TelemetryHandler) ListStreams(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	q := h.client.TelemetryStream.Query().
		Where(telemetrystream.TenantID(tenantID)).
		Order(ent.Desc(telemetrystream.FieldStartedAt))

	if memberIDStr := r.URL.Query().Get("member_id"); memberIDStr != "" {
		if memberID, err := uuid.Parse(memberIDStr); err == nil {
			q = q.Where(telemetrystream.FleetMemberID(memberID))
		}
	}
	if status := r.URL.Query().Get("status"); status != "" {
		q = q.Where(telemetrystream.Status(status))
	}

	streams, err := q.Limit(100).All(r.Context())
	if err != nil {
		h.log.Error("list streams", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, streams)
}

// ListPoints handles GET /api/v1/{tenant}/telemetry/streams/{streamId}/points
func (h *TelemetryHandler) ListPoints(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	streamID, err := uuid.Parse(chi.URLParam(r, "streamId"))
	if err != nil {
		http.Error(w, "invalid streamId", http.StatusBadRequest)
		return
	}

	// Verify stream belongs to this tenant
	if _, err := h.client.TelemetryStream.Query().
		Where(telemetrystream.ID(streamID), telemetrystream.TenantID(tenantID)).
		Only(r.Context()); err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	points, err := h.client.TelemetryPoint.Query().
		Where(telemetrypoint.StreamID(streamID)).
		Order(ent.Asc(telemetrypoint.FieldCapturedAt)).
		Limit(5000).
		All(r.Context())
	if err != nil {
		h.log.Error("list points", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, points)
}

// FleetRiderPosition is one rider's current known position, shaped to match
// @bengo-hub/maps' LiveFleetMap component (its FleetMap wrapper calls this endpoint for
// the map's initial snapshot; the component's own WebSocket layer for live push updates
// has no backend counterpart yet — see the fleet-map.tsx doc comment on the frontend).
type FleetRiderPosition struct {
	RiderID      string    `json:"rider_id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Latitude     float64   `json:"latitude"`
	Longitude    float64   `json:"longitude"`
	Heading      *float64  `json:"heading,omitempty"`
	Speed        *float64  `json:"speed,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
	ActiveTaskID *string   `json:"active_task_id,omitempty"`
}

// GetFleetTracking handles GET /api/v1/{tenant}/tracking/fleet
// Returns each active fleet member's most recently reported location (from their current
// telemetry stream), for the dispatcher's live fleet map. A rider with no active stream or
// no location point yet is simply omitted rather than reported at (0,0).
func (h *TelemetryHandler) GetFleetTracking(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	ctx := r.Context()

	members, err := h.client.FleetMember.Query().
		Where(
			fleetmember.TenantID(tenantID),
			fleetmember.StatusEQ("active"),
		).
		WithUser().
		All(ctx)
	if err != nil {
		h.log.Error("fleet tracking: list members", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	riders := make([]FleetRiderPosition, 0, len(members))
	for _, m := range members {
		stream, err := h.client.TelemetryStream.Query().
			Where(
				telemetrystream.TenantID(tenantID),
				telemetrystream.FleetMemberID(m.ID),
				telemetrystream.Status("active"),
			).
			Only(ctx)
		if err != nil {
			continue // no active stream — rider has reported no recent location
		}

		point, err := h.client.TelemetryPoint.Query().
			Where(telemetrypoint.StreamID(stream.ID)).
			Order(ent.Desc(telemetrypoint.FieldCapturedAt)).
			First(ctx)
		if err != nil {
			continue
		}

		lat, lng, ok := latLngFromMetadata(point.Metadata)
		if !ok {
			continue
		}

		name := "Rider"
		if u := m.Edges.User; u != nil && u.FullName != "" {
			name = u.FullName
		}

		pos := FleetRiderPosition{
			RiderID:   m.ID.String(),
			Name:      name,
			Status:    m.Status,
			Latitude:  lat,
			Longitude: lng,
			UpdatedAt: point.CapturedAt,
		}
		if point.SpeedKph != 0 {
			speed := point.SpeedKph
			pos.Speed = &speed
		}
		if point.BearingDeg != 0 {
			heading := point.BearingDeg
			pos.Heading = &heading
		}
		if activeTask, err := h.client.Task.Query().
			Where(
				task.TenantID(tenantID),
				task.StatusNotIn(terminalTaskStatuses...),
				task.HasAssignmentsWith(taskassignment.FleetMemberID(m.ID)),
			).
			Order(ent.Desc(task.FieldUpdatedAt)).
			First(ctx); err == nil {
			id := activeTask.ID.String()
			pos.ActiveTaskID = &id
		}

		riders = append(riders, pos)
	}

	respondJSON(w, http.StatusOK, map[string]any{"riders": riders})
}

// latLngFromMetadata extracts the lat/lng pair IngestLocation stores in a TelemetryPoint's
// JSON metadata (there are no dedicated lat/lng columns on that entity — see
// internal/modules/telemetry/service.go's IngestLocation).
func latLngFromMetadata(metadata map[string]any) (lat, lng float64, ok bool) {
	latVal, latOK := metadata["lat"].(float64)
	lngVal, lngOK := metadata["lng"].(float64)
	if !latOK || !lngOK || (latVal == 0 && lngVal == 0) {
		return 0, 0, false
	}
	return latVal, lngVal, true
}

// resolveFleetMember looks up the FleetMember UUID from the JWT subject (auth user ID).
func (h *TelemetryHandler) resolveFleetMember(r *http.Request, tenantID uuid.UUID) (uuid.UUID, error) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		return uuid.Nil, nil
	}
	authUserID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return uuid.Nil, nil
	}
	member, err := h.client.FleetMember.Query().
		Where(
			fleetmember.UserID(authUserID),
			fleetmember.TenantID(tenantID),
		).Only(r.Context())
	if err != nil {
		return uuid.Nil, err
	}
	return member.ID, nil
}
