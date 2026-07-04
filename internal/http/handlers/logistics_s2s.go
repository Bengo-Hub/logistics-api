package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

// S2S (service-to-service) dispatch handlers. Unlike the user-facing /tasks routes — which are
// RBAC-gated on a user Subject (and so reject the shared INTERNAL_SERVICE_KEY, which carries no
// user identity) — these are authenticated ONLY by the internal service key and take the tenant
// from the URL path. They let another backend (e.g. pos-api) create a delivery task and assign a
// rider directly, for orders logistics does not otherwise own (POS-native delivery orders).

// S2SCreateTask handles POST /api/v1/s2s/dispatch/{tenant}/tasks
// {tenant} is the tenant UUID. Body is tasks.CreateTaskRequest.
func (h *LogisticsHandler) S2SCreateTask(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenant"))
	if err != nil {
		http.Error(w, "invalid tenant", http.StatusBadRequest)
		return
	}

	var req tasks.CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.SourceService == "" {
		req.SourceService = "pos"
	}
	if req.TaskType == "" {
		req.TaskType = "delivery"
	}

	t, err := h.taskSvc.CreateTask(r.Context(), tenantID, req)
	if err != nil {
		h.log.Warn("s2s create task", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusCreated, t)
}

// S2SAssignTask handles POST /api/v1/s2s/dispatch/{tenant}/tasks/{taskId}/assign
// {tenant} is the tenant UUID. Body is tasks.AssignTaskRequest ({fleet_member_id}).
func (h *LogisticsHandler) S2SAssignTask(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenant"))
	if err != nil {
		http.Error(w, "invalid tenant", http.StatusBadRequest)
		return
	}
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}

	var req tasks.AssignTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	assignment, err := h.taskSvc.AssignTask(r.Context(), tenantID, taskID, req)
	if err != nil {
		h.log.Warn("s2s assign task", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusCreated, assignment)
}
