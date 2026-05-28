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
	"github.com/bengobox/logistics-service/internal/ent/telemetrypoint"
	"github.com/bengobox/logistics-service/internal/ent/telemetrystream"
	telemetrysvc "github.com/bengobox/logistics-service/internal/modules/telemetry"
)

// TelemetryHandler handles GPS telemetry ingestion and stream query endpoints.
type TelemetryHandler struct {
	log    *zap.Logger
	svc    *telemetrysvc.Service
	client *ent.Client
}

// NewTelemetryHandler creates a new TelemetryHandler.
func NewTelemetryHandler(log *zap.Logger, svc *telemetrysvc.Service, client *ent.Client) *TelemetryHandler {
	return &TelemetryHandler{
		log:    log.Named("telemetry.handler"),
		svc:    svc,
		client: client,
	}
}

// RegisterRoutes registers telemetry routes on the given tenant-scoped router.
func (h *TelemetryHandler) RegisterRoutes(r chi.Router) {
	r.Route("/telemetry", func(t chi.Router) {
		t.Get("/", h.GetSummary)   // GET /telemetry?period=today — summary for tracking/dashboard
		t.Post("/location", h.IngestLocation)
		t.Post("/stream/end", h.EndStream)
		t.Get("/streams", h.ListStreams)
		t.Get("/streams/{streamId}/points", h.ListPoints)
	})
}

// TelemetrySummary is returned by GET /telemetry.
type TelemetrySummary struct {
	Period                   string  `json:"period"`
	ActiveRiders             int     `json:"active_riders"`
	CompletedTasks           int     `json:"completed_tasks"`
	AvgDeliveryTimeMinutes   float64 `json:"avg_delivery_time_minutes"`
	ActiveStreams             int     `json:"active_streams"`
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
		ActiveStreams:           activeStreams,
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
