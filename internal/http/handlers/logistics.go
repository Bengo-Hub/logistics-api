package handlers

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/modules/fleet"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

// LogisticsHandler handles task and fleet HTTP endpoints.
type LogisticsHandler struct {
	log      *zap.Logger
	taskSvc  *tasks.Service
	fleetSvc *fleet.Service
}

// NewLogisticsHandler creates a new logistics handler.
func NewLogisticsHandler(log *zap.Logger, taskSvc *tasks.Service, fleetSvc *fleet.Service) *LogisticsHandler {
	return &LogisticsHandler{
		log:      log.Named("logistics.handler"),
		taskSvc:  taskSvc,
		fleetSvc: fleetSvc,
	}
}

// --- Tasks ---

// CreateTask handles POST /api/v1/{tenant}/tasks
func (h *LogisticsHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req tasks.CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	t, err := h.taskSvc.CreateTask(r.Context(), tenantID, req)
	if err != nil {
		h.log.Warn("create task", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusCreated, t)
}

// ListTasks handles GET /api/v1/{tenant}/tasks
func (h *LogisticsHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	filter := tasks.ListTasksFilter{
		Status: r.URL.Query().Get("status"),
		Limit:  50,
	}

	list, err := h.taskSvc.ListTasks(r.Context(), tenantID, filter)
	if err != nil {
		h.log.Error("list tasks", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, list)
}

// GetTask handles GET /api/v1/{tenant}/tasks/{taskId}
func (h *LogisticsHandler) GetTask(w http.ResponseWriter, r *http.Request) {
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

	t, err := h.taskSvc.GetTask(r.Context(), tenantID, taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	respondJSON(w, http.StatusOK, t)
}

// UpdateTaskStatus handles PATCH /api/v1/{tenant}/tasks/{taskId}/status
func (h *LogisticsHandler) UpdateTaskStatus(w http.ResponseWriter, r *http.Request) {
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

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Status == "" {
		http.Error(w, "status is required", http.StatusBadRequest)
		return
	}

	t, err := h.taskSvc.UpdateStatus(r.Context(), tenantID, taskID, body.Status)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusOK, t)
}

// AssignTask handles POST /api/v1/{tenant}/tasks/{taskId}/assign
func (h *LogisticsHandler) AssignTask(w http.ResponseWriter, r *http.Request) {
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

	var req tasks.AssignTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	assignment, err := h.taskSvc.AssignTask(r.Context(), tenantID, taskID, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusCreated, assignment)
}

// SubmitPoD handles POST /api/v1/{tenant}/tasks/{taskId}/pod
func (h *LogisticsHandler) SubmitPoD(w http.ResponseWriter, r *http.Request) {
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

	var req tasks.SubmitPoDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	pod, err := h.taskSvc.SubmitPoD(r.Context(), tenantID, taskID, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusCreated, pod)
}

// RateRider handles POST /api/v1/{tenant}/tasks/{taskId}/rate
func (h *LogisticsHandler) RateRider(w http.ResponseWriter, r *http.Request) {
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

	var body struct {
		Rating  int    `json:"rating"`
		Comment string `json:"comment,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Get the task to find the assigned rider
	t, err := h.taskSvc.GetTask(r.Context(), tenantID, taskID)
	if err != nil {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	// Find the rider assigned to this task
	assignments := t.Edges.Assignments
	if len(assignments) == 0 {
		http.Error(w, "no rider assigned to this task", http.StatusBadRequest)
		return
	}

	riderID := assignments[0].FleetMemberID

	// Get customer user ID from claims
	customerID := ""
	if claims, ok := authclient.ClaimsFromContext(r.Context()); ok {
		customerID = claims.Subject
	}

	// Extract order_id from external_reference
	orderID := ""
	if t.ExternalReference != "" {
		orderID = t.ExternalReference
	}

	rating, err := h.fleetSvc.RateRider(r.Context(), tenantID, riderID, fleet.RateRiderRequest{
		TaskID:         &taskID,
		OrderID:        orderID,
		CustomerUserID: customerID,
		Rating:         body.Rating,
		Comment:        body.Comment,
	})
	if err != nil {
		h.log.Error("rate rider", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusCreated, rating)
}

// --- Fleet ---

// GetFleet handles GET /api/v1/{tenant}/fleet
func (h *LogisticsHandler) GetFleet(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	claims, _ := authclient.ClaimsFromContext(r.Context())
	slug := ""
	if claims != nil {
		slug = claims.GetTenantSlug()
	}

	fl, err := h.fleetSvc.GetOrCreateFleet(r.Context(), tenantID, slug)
	if err != nil {
		h.log.Error("get fleet", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, fl)
}

// ListMembers handles GET /api/v1/{tenant}/fleet/members
func (h *LogisticsHandler) ListMembers(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	status := r.URL.Query().Get("status")

	members, err := h.fleetSvc.ListMembers(r.Context(), tenantID, status)
	if err != nil {
		h.log.Error("list fleet members", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, members)
}

// GetMember handles GET /api/v1/{tenant}/fleet/members/{memberId}
func (h *LogisticsHandler) GetMember(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	memberID, err := uuid.Parse(chi.URLParam(r, "memberId"))
	if err != nil {
		http.Error(w, "invalid member id", http.StatusBadRequest)
		return
	}

	m, err := h.fleetSvc.GetMember(r.Context(), tenantID, memberID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	respondJSON(w, http.StatusOK, m)
}

// InviteMember handles POST /api/v1/{tenant}/fleet/members
func (h *LogisticsHandler) InviteMember(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	claims, _ := authclient.ClaimsFromContext(r.Context())
	slug := ""
	if claims != nil {
		slug = claims.GetTenantSlug()
	}

	// Support both formats: {email, id_number} or {user_id, ...}
	var raw map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var m *ent.FleetMember
	var err error

	email, hasEmail := raw["email"].(string)
	userIDStr, hasUserID := raw["user_id"].(string)

	if hasEmail && email != "" && (!hasUserID || userIDStr == "") {
		// Simplified invite by email
		idNumber, _ := raw["id_number"].(string)
		m, err = h.fleetSvc.InviteMemberByEmail(r.Context(), tenantID, slug, fleet.InviteByEmailRequest{
			Email:    email,
			IDNumber: idNumber,
		})
	} else {
		// Legacy invite by user_id
		rawJSON, _ := json.Marshal(raw)
		var req fleet.InviteMemberRequest
		if jsonErr := json.Unmarshal(rawJSON, &req); jsonErr != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		m, err = h.fleetSvc.InviteMember(r.Context(), tenantID, slug, req)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusCreated, m)
}

// ApproveMember handles POST /api/v1/{tenant}/fleet/members/{memberId}/approve
func (h *LogisticsHandler) ApproveMember(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	memberID, err := uuid.Parse(chi.URLParam(r, "memberId"))
	if err != nil {
		http.Error(w, "invalid member id", http.StatusBadRequest)
		return
	}

	m, err := h.fleetSvc.ApproveMember(r.Context(), tenantID, memberID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusOK, m)
}

// SuspendMember handles POST /api/v1/{tenant}/fleet/members/{memberId}/suspend
func (h *LogisticsHandler) SuspendMember(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	memberID, err := uuid.Parse(chi.URLParam(r, "memberId"))
	if err != nil {
		http.Error(w, "invalid member id", http.StatusBadRequest)
		return
	}

	m, err := h.fleetSvc.SuspendMember(r.Context(), tenantID, memberID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusOK, m)
}

// --- helpers ---
// tenantIDFromClaims is now defined in tenant.go with platform-owner override support.

