package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/task"
)

// AnalyticsHandler handles KPI and analytics endpoints.
type AnalyticsHandler struct {
	client *ent.Client
	log    *zap.Logger
}

// NewAnalyticsHandler creates a new AnalyticsHandler.
func NewAnalyticsHandler(client *ent.Client, log *zap.Logger) *AnalyticsHandler {
	return &AnalyticsHandler{client: client, log: log.Named("analytics")}
}

// RegisterRoutes registers analytics routes under the tenant router.
func (h *AnalyticsHandler) RegisterRoutes(r chi.Router) {
	r.Route("/analytics", func(a chi.Router) {
		a.Get("/kpis", h.GetKPIs)
	})
}

// KPIResponse is the response body for GET /{tenant}/analytics/kpis.
type KPIResponse struct {
	Period          string  `json:"period"`
	TotalTasks      int     `json:"total_tasks"`
	PendingTasks    int     `json:"pending_tasks"`
	ActiveTasks     int     `json:"active_tasks"`
	CompletedTasks  int     `json:"completed_tasks"`
	FailedTasks     int     `json:"failed_tasks"`
	CancelledTasks  int     `json:"cancelled_tasks"`
	OnTimePercent   float64 `json:"on_time_percent"`
	ActiveRiders    int     `json:"active_riders"`
	TotalRiders     int     `json:"total_riders"`
	UtilizationPct  float64 `json:"utilization_percent"`
	AvgDeliveryMins float64 `json:"avg_delivery_minutes"`
}

// GetKPIs handles GET /{tenant}/analytics/kpis
// Query params: period=7d|30d|today (default: 7d)
func (h *AnalyticsHandler) GetKPIs(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	periodStr := r.URL.Query().Get("period")
	if periodStr == "" {
		periodStr = "7d"
	}

	var since time.Time
	switch periodStr {
	case "today":
		now := time.Now()
		since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	case "30d":
		since = time.Now().AddDate(0, 0, -30)
	default: // 7d
		since = time.Now().AddDate(0, 0, -7)
		periodStr = "7d"
	}

	ctx := r.Context()

	// Task counts by status
	activeStatuses := []string{"assigned", "accepted", "en_route", "en_route_pickup",
		"arrived_pickup", "picked_up", "en_route_dropoff", "arrived_dropoff"}

	allTasks, err := h.client.Task.Query().
		Where(task.TenantID(tenantID), task.CreatedAtGTE(since)).
		All(ctx)
	if err != nil {
		h.log.Error("analytics: query tasks", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var (
		pending, active, completed, failed, cancelled int
		onTimeCount, slaCount                         int
		totalDeliveryMins                             float64
		deliveryCount                                 int
	)

	for _, t := range allTasks {
		switch t.Status {
		case "pending":
			pending++
		case "completed", "delivered":
			completed++
			// On-time: completed before SLA due date
			if t.SLADueAt != nil && !t.UpdatedAt.IsZero() {
				slaCount++
				if t.UpdatedAt.Before(*t.SLADueAt) {
					onTimeCount++
				}
			}
			// Average delivery time: created_at → updated_at (completion)
			if !t.CreatedAt.IsZero() && !t.UpdatedAt.IsZero() {
				mins := t.UpdatedAt.Sub(t.CreatedAt).Minutes()
				if mins > 0 && mins < 1440 { // ignore outliers >24h
					totalDeliveryMins += mins
					deliveryCount++
				}
			}
		case "failed":
			failed++
		case "cancelled":
			cancelled++
		default:
			for _, s := range activeStatuses {
				if t.Status == s {
					active++
					break
				}
			}
		}
	}

	total := len(allTasks)

	var onTimePct float64
	if slaCount > 0 {
		onTimePct = float64(onTimeCount) / float64(slaCount) * 100
	}

	var avgDeliveryMins float64
	if deliveryCount > 0 {
		avgDeliveryMins = totalDeliveryMins / float64(deliveryCount)
	}

	// Fleet metrics
	totalRiders, _ := h.client.FleetMember.Query().
		Where(fleetmember.TenantID(tenantID)).
		Count(ctx)

	activeRiders, _ := h.client.FleetMember.Query().
		Where(fleetmember.TenantID(tenantID), fleetmember.Status("active")).
		Count(ctx)

	var utilizationPct float64
	if totalRiders > 0 {
		utilizationPct = float64(activeRiders) / float64(totalRiders) * 100
	}

	respondJSON(w, http.StatusOK, KPIResponse{
		Period:          periodStr,
		TotalTasks:      total,
		PendingTasks:    pending,
		ActiveTasks:     active,
		CompletedTasks:  completed,
		FailedTasks:     failed,
		CancelledTasks:  cancelled,
		OnTimePercent:   onTimePct,
		ActiveRiders:    activeRiders,
		TotalRiders:     totalRiders,
		UtilizationPct:  utilizationPct,
		AvgDeliveryMins: avgDeliveryMins,
	})
}
