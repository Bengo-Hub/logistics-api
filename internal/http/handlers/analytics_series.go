package handlers

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Bengo-Hub/httpware"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/shared/localtime"
)

// reportLocation is the time zone reports count days in (localtime.Location).
var reportLocation = localtime.Location

// periodDays are the report periods in calendar days, today included.
var periodDays = map[string]int{"today": 1, "7d": 7, "30d": 30, "90d": 90}

// analyticsPeriod reads ?period= (today, 7d, 30d, 90d; def when missing or unknown) and returns
// it with its start: midnight in reportLocation, the given number of calendar days back.
func analyticsPeriod(r *http.Request, def string) (string, time.Time) {
	p := r.URL.Query().Get("period")
	if _, ok := periodDays[p]; !ok {
		p = def
	}
	days, ok := periodDays[p]
	if !ok {
		p, days = "7d", 7
	}
	now := time.Now().In(reportLocation)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, reportLocation)
	return p, midnight.AddDate(0, 0, -(days - 1))
}

// analyticsOutlet is the outlet the dashboard is drilled into (X-Outlet-ID), or uuid.Nil for
// every outlet of the tenant. An unreadable header means no drill-down.
func analyticsOutlet(r *http.Request) uuid.UUID {
	id, err := uuid.Parse(httpware.GetOutletID(r.Context()))
	if err != nil {
		return uuid.Nil
	}
	return id
}

// outletFilter adds "AND t.outlet_id = $n" to a tasks query when a drill-down is set. The
// clause is only present then, so the planner never sees an optional-parameter OR and keeps
// choosing the (tenant_id, outlet_id, created_at) index for a drill-down and
// (tenant_id, created_at) otherwise. args are the query's arguments so far.
func outletFilter(outlet uuid.UUID, args []any) (string, []any) {
	if outlet == uuid.Nil {
		return "", args
	}
	args = append(args, outlet)
	return fmt.Sprintf(" AND t.outlet_id = $%d", len(args)), args
}

// SeriesPoint is one hour (today) or day of task outcomes, by when the task was created.
type SeriesPoint struct {
	Start     time.Time `json:"start"`
	Total     int       `json:"total"`
	Delivered int       `json:"delivered"`
	Failed    int       `json:"failed"`
	Cancelled int       `json:"cancelled"`
}

// GetSeries godoc
// @Summary Deliveries over time
// @Description Task outcomes per hour (today) or per day, counted in SQL with empty buckets filled.
// @Tags Analytics
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param period query string false "today | 7d | 30d | 90d (default 7d)"
// @Success 200 {object} map[string]interface{}
// @Router /{tenant}/analytics/series [get]
func (h *AnalyticsHandler) GetSeries(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	period, since := analyticsPeriod(r, "7d")
	unit := "day"
	if period == "today" {
		unit = "hour"
	}
	points := map[int64]SeriesPoint{}
	if h.db != nil {
		// One grouped scan served by the (tenant_id, created_at) index, or the
		// (tenant_id, outlet_id, created_at) one when drilled into an outlet.
		scope, args := outletFilter(analyticsOutlet(r), []any{tenantID, since, unit, reportLocation.String()})
		rows, err := h.db.QueryContext(r.Context(), `
			SELECT date_trunc($3, t.created_at AT TIME ZONE $4) AS bucket,
			  count(*),
			  count(*) FILTER (WHERE t.status IN ('delivered', 'completed')),
			  count(*) FILTER (WHERE t.status = 'failed'),
			  count(*) FILTER (WHERE t.status = 'cancelled')
			FROM tasks t
			WHERE t.tenant_id = $1 AND t.created_at >= $2`+scope+`
			GROUP BY 1`, args...)
		if err != nil {
			h.log.Error("analytics: series", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var local time.Time
			var p SeriesPoint
			if err := rows.Scan(&local, &p.Total, &p.Delivered, &p.Failed, &p.Cancelled); err != nil {
				h.log.Error("analytics: series scan", zap.Error(err))
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			// The bucket is wall-clock time in reportLocation (timestamp without time zone).
			p.Start = time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, reportLocation)
			points[p.Start.Unix()] = p
		}
		if err := rows.Err(); err != nil {
			h.log.Error("analytics: series rows", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	now := time.Now().In(reportLocation)
	out := []SeriesPoint{}
	for t := since; !t.After(now); {
		if p, ok := points[t.Unix()]; ok {
			out = append(out, p)
		} else {
			out = append(out, SeriesPoint{Start: t})
		}
		if unit == "hour" {
			t = t.Add(time.Hour)
		} else {
			t = t.AddDate(0, 0, 1)
		}
	}
	respondJSON(w, http.StatusOK, map[string]any{"period": period, "unit": unit, "points": out})
}

// RiderStat is one rider's jobs in the period (the rider who held each job at the end).
type RiderStat struct {
	FleetMemberID   string  `json:"fleet_member_id"`
	Name            string  `json:"name"`
	Jobs            int     `json:"jobs"`
	Delivered       int     `json:"delivered"`
	Failed          int     `json:"failed"`
	AvgDeliveryMins float64 `json:"avg_delivery_minutes"`
	OnTimePercent   float64 `json:"on_time_percent"`
	AverageRating   float64 `json:"average_rating"`
	Ratings         int     `json:"ratings"`
}

// GetRiderStats godoc
// @Summary Rider performance
// @Description Jobs, deliveries, failures, delivery time and on-time share per rider for the period, aggregated in SQL (top 50 by deliveries).
// @Tags Analytics
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param period query string false "today | 7d | 30d | 90d (default 7d)"
// @Success 200 {array} RiderStat
// @Router /{tenant}/analytics/riders [get]
func (h *AnalyticsHandler) GetRiderStats(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	out := []RiderStat{}
	if h.db == nil {
		respondJSON(w, http.StatusOK, out)
		return
	}
	_, since := analyticsPeriod(r, "7d")
	scope, args := outletFilter(analyticsOutlet(r), []any{tenantID, since})
	// The rider credited with a job is the one whose assignment was not declined or taken back.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT a.fleet_member_id,
		  count(*),
		  count(*) FILTER (WHERE t.status IN ('delivered', 'completed')),
		  count(*) FILTER (WHERE t.status = 'failed'),
		  COALESCE(avg(EXTRACT(EPOCH FROM (p.captured_at - t.created_at)) / 60)
		      FILTER (WHERE p.captured_at - t.created_at BETWEEN interval '0' AND interval '24 hours'), 0),
		  count(*) FILTER (WHERE t.sla_due_at IS NOT NULL AND p.captured_at IS NOT NULL),
		  count(*) FILTER (WHERE t.sla_due_at IS NOT NULL AND p.captured_at <= t.sla_due_at)
		FROM tasks t
		JOIN task_assignments a ON a.task_id = t.id AND a.status NOT IN ('declined', 'unassigned')
		LEFT JOIN proof_of_deliveries p ON p.task_id = t.id
		WHERE t.tenant_id = $1 AND t.created_at >= $2`+scope+`
		GROUP BY 1
		ORDER BY 3 DESC, 2 DESC
		LIMIT 50`, args...)
	if err != nil {
		h.log.Error("analytics: rider stats", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var s RiderStat
		var id uuid.UUID
		var slaCount, onTime int
		if err := rows.Scan(&id, &s.Jobs, &s.Delivered, &s.Failed, &s.AvgDeliveryMins, &slaCount, &onTime); err != nil {
			h.log.Error("analytics: rider stats scan", zap.Error(err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s.FleetMemberID = id.String()
		if slaCount > 0 {
			s.OnTimePercent = float64(onTime) / float64(slaCount) * 100
		}
		ids = append(ids, id)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		h.log.Error("analytics: rider stats rows", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Names and ratings for the page in one query.
	if len(ids) > 0 {
		members, err := h.client.FleetMember.Query().
			Where(fleetmember.TenantID(tenantID), fleetmember.IDIn(ids...)).
			WithUser().
			All(r.Context())
		if err != nil {
			h.log.Warn("analytics: rider names", zap.Error(err))
		}
		byID := make(map[string]int, len(out))
		for i := range out {
			byID[out[i].FleetMemberID] = i
		}
		for _, m := range members {
			i, ok := byID[m.ID.String()]
			if !ok {
				continue
			}
			out[i].AverageRating, out[i].Ratings = m.AverageRating, m.TotalRatings
			if u := m.Edges.User; u != nil {
				out[i].Name = u.FullName
				if out[i].Name == "" {
					out[i].Name = u.Email
				}
			}
		}
	}
	respondJSON(w, http.StatusOK, out)
}
