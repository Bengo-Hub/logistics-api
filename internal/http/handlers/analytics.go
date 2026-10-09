package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

// AnalyticsHandler handles KPI and analytics endpoints.
type AnalyticsHandler struct {
	db     *sql.DB
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
		a.Get("/zones", h.GetZoneStats)
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

	// Counts by status in one GROUP BY, and on-time/average from proof of delivery, all in SQL.
	// The old version loaded every task of the period into memory, and measured delivery time
	// to the task's last update and "on time" against that, not the actual hand-over time.
	var byStatus []struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}
	if err := h.client.Task.Query().
		Where(task.TenantID(tenantID), task.CreatedAtGTE(since)).
		GroupBy(task.FieldStatus).
		Aggregate(ent.Count()).
		Scan(ctx, &byStatus); err != nil {
		h.log.Error("analytics: count tasks", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	k := kpiFromCounts(byStatus)

	var onTimePct, avgDeliveryMins float64
	if h.db != nil {
		var slaCount, onTime int
		var avg sql.NullFloat64
		if err := h.db.QueryRowContext(ctx, `
			SELECT
			  count(*) FILTER (WHERE t.sla_due_at IS NOT NULL),
			  count(*) FILTER (WHERE t.sla_due_at IS NOT NULL AND p.captured_at <= t.sla_due_at),
			  avg(EXTRACT(EPOCH FROM (p.captured_at - t.created_at)) / 60)
			    FILTER (WHERE p.captured_at - t.created_at BETWEEN interval '0' AND interval '24 hours')
			FROM tasks t
			JOIN proof_of_deliveries p ON p.task_id = t.id
			WHERE t.tenant_id = $1 AND t.created_at >= $2`, tenantID, since).Scan(&slaCount, &onTime, &avg); err != nil {
			h.log.Warn("analytics: delivery timing", zap.Error(err))
		}
		if slaCount > 0 {
			onTimePct = float64(onTime) / float64(slaCount) * 100
		}
		avgDeliveryMins = avg.Float64
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
		TotalTasks:      k.total,
		PendingTasks:    k.pending,
		ActiveTasks:     k.active,
		CompletedTasks:  k.completed,
		FailedTasks:     k.failed,
		CancelledTasks:  k.cancelled,
		OnTimePercent:   onTimePct,
		ActiveRiders:    activeRiders,
		TotalRiders:     totalRiders,
		UtilizationPct:  utilizationPct,
		AvgDeliveryMins: avgDeliveryMins,
	})
}

type taskCounts struct {
	total, pending, active, completed, failed, cancelled int
}

// kpiFromCounts folds per-status counts into the dashboard buckets.
func kpiFromCounts(rows []struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}) taskCounts {
	var k taskCounts
	for _, r := range rows {
		k.total += r.Count
		switch {
		case r.Status == "pending":
			k.pending += r.Count
		case r.Status == "delivered" || r.Status == "completed":
			k.completed += r.Count
		case r.Status == "failed":
			k.failed += r.Count
		case r.Status == "cancelled":
			k.cancelled += r.Count
		case tasks.IsInProgress(r.Status):
			k.active += r.Count
		}
	}
	return k
}

// ZoneStat is one delivery area's performance over a period.
type ZoneStat struct {
	ZoneID          string  `json:"zone_id"`
	ZoneName        string  `json:"zone_name"`
	Tasks           int     `json:"tasks"`
	Delivered       int     `json:"delivered"`
	Failed          int     `json:"failed"`
	Cancelled       int     `json:"cancelled"`
	DeliveryFees    float64 `json:"delivery_fees"`
	AvgDistanceKm   float64 `json:"avg_distance_km"`
	AvgDeliveryMins float64 `json:"avg_delivery_minutes"`
	OnTimePercent   float64 `json:"on_time_percent"`
}

// GetZoneStats godoc
// @Summary Deliveries by zone
// @Description Task counts, fees, distance and timing grouped by delivery area, aggregated in SQL.
// @Tags Analytics
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param period query string false "today | 7d | 30d | 90d (default 30d)"
// @Success 200 {array} ZoneStat
// @Router /{tenant}/analytics/zones [get]
func (h *AnalyticsHandler) GetZoneStats(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if h.db == nil {
		respondJSON(w, http.StatusOK, []ZoneStat{})
		return
	}
	since := time.Now().AddDate(0, 0, -30)
	switch r.URL.Query().Get("period") {
	case "today":
		now := time.Now()
		since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	case "7d":
		since = time.Now().AddDate(0, 0, -7)
	case "90d":
		since = time.Now().AddDate(0, 0, -90)
	}
	// One grouped scan over the tenant's tasks of the period, served by the
	// (tenant_id, created_at) index; tasks without a zone report as "Unzoned".
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT
		  COALESCE(t.metadata->>'zone_id', '') AS zone_id,
		  COALESCE(max(t.metadata->>'zone_name'), 'Unzoned') AS zone_name,
		  count(*),
		  count(*) FILTER (WHERE t.status IN ('delivered', 'completed')),
		  count(*) FILTER (WHERE t.status = 'failed'),
		  count(*) FILTER (WHERE t.status = 'cancelled'),
		  COALESCE(sum((t.metadata->>'delivery_fee')::numeric) FILTER (WHERE t.status IN ('delivered', 'completed')
		      AND (t.metadata->>'delivery_fee') ~ '^[0-9.]+$'), 0),
		  COALESCE(avg((t.metadata->>'distance_km')::numeric) FILTER (WHERE (t.metadata->>'distance_km') ~ '^[0-9.]+$'), 0),
		  COALESCE(avg(EXTRACT(EPOCH FROM (p.captured_at - t.created_at)) / 60)
		      FILTER (WHERE p.captured_at - t.created_at BETWEEN interval '0' AND interval '24 hours'), 0),
		  count(*) FILTER (WHERE t.sla_due_at IS NOT NULL AND p.captured_at IS NOT NULL),
		  count(*) FILTER (WHERE t.sla_due_at IS NOT NULL AND p.captured_at <= t.sla_due_at)
		FROM tasks t
		LEFT JOIN proof_of_deliveries p ON p.task_id = t.id
		WHERE t.tenant_id = $1 AND t.created_at >= $2 AND t.task_type = 'delivery'
		GROUP BY 1
		ORDER BY 3 DESC
		LIMIT 500`, tenantID, since)
	if err != nil {
		h.log.Error("analytics: zone stats", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := []ZoneStat{}
	for rows.Next() {
		var zs ZoneStat
		var slaCount, onTime int
		if err := rows.Scan(&zs.ZoneID, &zs.ZoneName, &zs.Tasks, &zs.Delivered, &zs.Failed, &zs.Cancelled,
			&zs.DeliveryFees, &zs.AvgDistanceKm, &zs.AvgDeliveryMins, &slaCount, &onTime); err != nil {
			h.log.Error("analytics: zone stats scan", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if slaCount > 0 {
			zs.OnTimePercent = float64(onTime) / float64(slaCount) * 100
		}
		out = append(out, zs)
	}
	respondJSON(w, http.StatusOK, out)
}

// SetDB wires the SQL handle used for the delivery timing aggregate.
func (h *AnalyticsHandler) SetDB(db *sql.DB) { h.db = db }