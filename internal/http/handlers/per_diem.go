package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	"github.com/bengobox/logistics-service/internal/modules/perdiem"
)

// Per diem for staff riders is an erp-api expense claim. These endpoints let an admin or the
// rider raise it for a completed trip (with the days for a multi-day trip), or raise it again
// after erp-api was unreachable. The claim is idempotent per task in erp-api.

// SetPerDiem wires the per diem service. Optional: nil hides the endpoints' effect (503).
func (h *LogisticsHandler) SetPerDiem(s *perdiem.Service) { h.perDiem = s }

type perDiemBody struct {
	// Days on the trip; 0 lets HR derive it from the trip's start and end.
	Days float64 `json:"days"`
}

func (h *LogisticsHandler) raisePerDiem(w http.ResponseWriter, r *http.Request, tenantID, taskID, memberID uuid.UUID) {
	if !h.perDiem.Enabled() {
		http.Error(w, "HR payroll integration is not configured", http.StatusServiceUnavailable)
		return
	}
	var body perDiemBody
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Days < 0 || body.Days > 60 {
			http.Error(w, "days must be between 0 and 60", http.StatusBadRequest)
			return
		}
	}
	out, err := h.perDiem.RaiseForTask(r.Context(), tenantID, taskID, memberID, body.Days)
	if errors.Is(err, perdiem.ErrNotStaff) {
		http.Error(w, "per diem applies to staff (payroll) riders only", http.StatusUnprocessableEntity)
		return
	}
	if err != nil {
		h.log.Warn("raise per diem", zap.Error(err))
		http.Error(w, "could not raise the per diem claim", http.StatusBadGateway)
		return
	}
	respondJSON(w, http.StatusOK, out)
}

// RaisePerDiem handles POST /api/v1/{tenant}/tasks/{taskId}/per-diem for dispatchers and
// admins; the claim goes to the rider who completed (or last held) the task.
func (h *LogisticsHandler) RaisePerDiem(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}
	if ok, terr := h.taskSvc.Client().Task.Query().Where(task.ID(taskID), task.TenantID(tenantID)).Exist(r.Context()); terr != nil || !ok {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	a, err := h.taskSvc.Client().TaskAssignment.Query().
		Where(taskassignment.TaskID(taskID)).
		Order(ent.Desc(taskassignment.FieldAssignedAt)).
		First(r.Context())
	if err != nil {
		http.Error(w, "task has no rider", http.StatusUnprocessableEntity)
		return
	}
	h.raisePerDiem(w, r, tenantID, taskID, a.FleetMemberID)
}

// RaiseMyPerDiem handles POST /api/v1/{tenant}/riders/me/tasks/{taskId}/per-diem: a staff
// rider claims per diem for a trip they completed.
func (h *LogisticsHandler) RaiseMyPerDiem(w http.ResponseWriter, r *http.Request) {
	tenantID, member, ok := h.currentRider(w, r)
	if !ok {
		return
	}
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}
	held, err := h.taskSvc.Client().TaskAssignment.Query().
		Where(taskassignment.TaskID(taskID), taskassignment.FleetMemberID(member.ID)).
		Exist(r.Context())
	if err != nil || !held {
		http.Error(w, "not your trip", http.StatusForbidden)
		return
	}
	h.raisePerDiem(w, r, tenantID, taskID, member.ID)
}
