package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	httpware "github.com/Bengo-Hub/httpware"
	"github.com/Bengo-Hub/pagination"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/proofofdelivery"
	"github.com/bengobox/logistics-service/internal/modules/dispatch"
	"github.com/bengobox/logistics-service/internal/modules/fleet"
	"github.com/bengobox/logistics-service/internal/modules/perdiem"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
	"github.com/bengobox/logistics-service/internal/platform/subscriptions"
)

// TaskDispatcher is the subset of dispatch.AutoDispatcher used by the handler.
type TaskDispatcher interface {
	DispatchTask(ctx context.Context, tenantID, taskID uuid.UUID) error
}

// LogisticsHandler handles task and fleet HTTP endpoints.
type LogisticsHandler struct {
	log        *zap.Logger
	taskSvc    *tasks.Service
	fleetSvc   *fleet.Service
	dispatcher TaskDispatcher
	perms      permissionChecker
	tracker    *dispatch.AutoDispatcher
	perDiem    *perdiem.Service
}

// NewLogisticsHandler creates a new logistics handler.
func NewLogisticsHandler(log *zap.Logger, taskSvc *tasks.Service, fleetSvc *fleet.Service, dispatcher TaskDispatcher) *LogisticsHandler {
	return &LogisticsHandler{
		log:        log.Named("logistics.handler"),
		taskSvc:    taskSvc,
		fleetSvc:   fleetSvc,
		dispatcher: dispatcher,
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

	p := pagination.Parse(r)
	q := r.URL.Query()
	filter := tasks.ListTasksFilter{
		Limit:  p.Limit,
		Offset: p.Offset,
		Search: strings.TrimSpace(q.Get("search")),
	}

	// status accepts either a single value (exact match) or a comma-separated list (OR-matched)
	// -- the frontend's coarse "En Route" tab sends "en_route_pickup,en_route_dropoff" since no
	// single granular FSM status means "en route" on its own.
	if statusParam := q.Get("status"); statusParam != "" {
		if strings.Contains(statusParam, ",") {
			filter.Statuses = strings.Split(statusParam, ",")
		} else {
			filter.Status = statusParam
		}
	}

	if v := q.Get("date_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.DateFrom = &t
		}
	}
	if v := q.Get("date_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.DateTo = &t
		}
	}

	// Apply outlet context filter if X-Outlet-ID was sent
	if outletIDStr := httpware.GetOutletID(r.Context()); outletIDStr != "" {
		if outletUID, err := uuid.Parse(outletIDStr); err == nil {
			filter.OutletID = &outletUID
		}
	}

	list, total, err := h.taskSvc.ListTasks(r.Context(), tenantID, filter)
	if err != nil {
		h.log.Error("list tasks", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, pagination.NewResponse(toTaskResponses(list), total, p))
}

// ListMyTasks handles GET /api/v1/{tenant}/riders/me/tasks
// It resolves the current rider's fleet member from the JWT subject (auth user ID)
// + tenant — mirroring GetMyEarnings — and returns ONLY that member's tasks,
// paginated, in the same envelope as ListTasks ({data, total, limit, page, hasMore}).
// Supports ?status=, ?limit=, ?offset= / ?page=.
func (h *LogisticsHandler) ListMyTasks(w http.ResponseWriter, r *http.Request) {
	tenantID, member, ok := h.currentRider(w, r)
	if !ok {
		return
	}

	p := pagination.Parse(r)
	filter := tasks.ListTasksFilter{
		Status:   r.URL.Query().Get("status"),
		MemberID: member.ID,
		Limit:    p.Limit,
		Offset:   p.Offset,
	}

	list, total, err := h.taskSvc.ListTasks(r.Context(), tenantID, filter)
	if err != nil {
		h.log.Error("list my tasks", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, pagination.NewResponse(toTaskResponses(list), total, p))
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
	// A rider sees their own jobs and open ones they could claim, not other customers' orders.
	if member := h.riderOnly(r, tenantID); member != nil && !h.riderMayView(r.Context(), t, member) {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}

	respondJSON(w, http.StatusOK, toTaskResponse(t))
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
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Status == "" {
		http.Error(w, "status is required", http.StatusBadRequest)
		return
	}

	if !h.callerMayWorkTask(w, r, tenantID, taskID) {
		return
	}

	actor := h.actor(r, "dispatcher")
	if member := h.riderOnly(r, tenantID); member != nil {
		actor = tasks.Actor{ID: member.ID, Type: "rider"}
	}
	t, err := h.taskSvc.UpdateStatusAs(r.Context(), tenantID, taskID, body.Status, body.Reason, actor)
	if err != nil {
		writeTaskError(w, err)
		return
	}

	respondJSON(w, http.StatusOK, toTaskResponse(t))
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
		writeTaskError(w, err)
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

	if !h.callerMayWorkTask(w, r, tenantID, taskID) {
		return
	}
	// The photo is a private upload (customer doorsteps); keep the plain URL and sign on read.
	req.PhotoURL = httpware.StripMediaSignature(req.PhotoURL)
	req.SignatureURL = httpware.StripMediaSignature(req.SignatureURL)

	pod, err := h.taskSvc.SubmitPoD(r.Context(), tenantID, taskID, req)
	if err != nil {
		writeTaskError(w, err)
		return
	}

	respondJSON(w, http.StatusCreated, signPoDMedia(pod))
}

// callerMayWorkTask stops a rider from moving or delivering a task that is not theirs. Riders hold
// logistics.tasks.manage (it is what lets them update their own jobs), so without this any rider in
// the tenant could mark another rider's order picked up or delivered. Dispatchers and admins (users
// who are not fleet members) are unaffected. Writes a 403 and returns false when refused.
func (h *LogisticsHandler) callerMayWorkTask(w http.ResponseWriter, r *http.Request, tenantID, taskID uuid.UUID) bool {
	member := h.riderOnly(r, tenantID)
	if member == nil {
		return true // a dispatcher/admin acting on the board, or a service call
	}
	if assignee, assigned := h.taskSvc.ActiveAssignee(r.Context(), taskID); assigned && assignee == member.ID {
		return true
	}
	http.Error(w, "this delivery is assigned to another rider", http.StatusForbidden)
	return false
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
	customerID := ""
	if claims, ok := authclient.ClaimsFromContext(r.Context()); ok {
		customerID = claims.Subject
	}
	h.rateTaskRider(w, r, tenantID, taskID, customerID, body.Rating, body.Comment)
}

// S2SRateRider handles POST /api/v1/s2s/dispatch/{tenant}/tasks/{taskId}/rate (service key): the
// customer's rider rating relayed by ordering, which owns the customer and the order.
func (h *LogisticsHandler) S2SRateRider(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		Rating         int    `json:"rating"`
		Comment        string `json:"comment,omitempty"`
		CustomerUserID string `json:"customer_user_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	h.rateTaskRider(w, r, tenantID, taskID, body.CustomerUserID, body.Rating, body.Comment)
}

// rateTaskRider records a rating for the rider assigned to a task (shared by the tenant and S2S routes).
func (h *LogisticsHandler) rateTaskRider(w http.ResponseWriter, r *http.Request, tenantID, taskID uuid.UUID, customerID string, ratingValue int, comment string) {
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

	// Extract order_id from external_reference
	orderID := ""
	if t.ExternalReference != "" {
		orderID = t.ExternalReference
	}

	rating, err := h.fleetSvc.RateRider(r.Context(), tenantID, riderID, fleet.RateRiderRequest{
		TaskID:         &taskID,
		OrderID:        orderID,
		CustomerUserID: customerID,
		Rating:         ratingValue,
		Comment:        comment,
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

	p := pagination.Parse(r)
	status := r.URL.Query().Get("status")
	search := r.URL.Query().Get("search")

	members, total, err := h.fleetSvc.ListMembers(r.Context(), tenantID, status, search, p.Limit, p.Offset)
	if err != nil {
		h.log.Error("list fleet members", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, pagination.NewResponse(toFleetMemberResponses(members), total, p))
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

	respondJSON(w, http.StatusOK, toFleetMemberResponse(m))
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

	if count, cerr := h.fleetSvc.CountActiveMembers(r.Context(), tenantID); cerr == nil {
		if !subscriptions.CheckStructuralLimit(w, r, "riders", subscriptions.LimitRiders, count) {
			return
		}
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

	rawJSON, _ := json.Marshal(raw)
	if hasEmail && email != "" && (!hasUserID || userIDStr == "") {
		// Invite by email: name, phone and employment terms are kept on the stub user.
		var req fleet.InviteByEmailRequest
		if jsonErr := json.Unmarshal(rawJSON, &req); jsonErr != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		m, err = h.fleetSvc.InviteMemberByEmail(r.Context(), tenantID, slug, req)
	} else {
		// Invite an existing user by id
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

	respondJSON(w, http.StatusCreated, signFleetMemberMedia(m))
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

	respondJSON(w, http.StatusOK, signFleetMemberMedia(m))
}

// SetMemberEmployment handles PUT /api/v1/{tenant}/fleet/members/{memberId}/employment.
// Body: {"type":"freelance|staff","per_task_earnings":bool?}. Staff riders draw a salary on
// erp-api payroll; their per diem is an erp-api claim, so nothing else is stored here.
func (h *LogisticsHandler) SetMemberEmployment(w http.ResponseWriter, r *http.Request) {
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
	var in fleet.Employment
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	m, err := h.fleetSvc.SetEmployment(r.Context(), tenantID, memberID, in)
	if ent.IsNotFound(err) {
		http.Error(w, "member not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusOK, signFleetMemberMedia(m))
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

	respondJSON(w, http.StatusOK, signFleetMemberMedia(m))
}

// RejectMember handles POST /api/v1/{tenant}/fleet/members/{memberId}/reject
func (h *LogisticsHandler) RejectMember(w http.ResponseWriter, r *http.Request) {
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

	var body struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.Reason = ""
	}

	m, err := h.fleetSvc.RejectMember(r.Context(), tenantID, memberID, body.Reason)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusOK, signFleetMemberMedia(m))
}

// DeleteMember handles DELETE /api/v1/{tenant}/fleet/members/{memberId}
func (h *LogisticsHandler) DeleteMember(w http.ResponseWriter, r *http.Request) {
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

	if err := h.fleetSvc.DeleteMember(r.Context(), tenantID, memberID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// BatchInviteMembers handles POST /api/v1/{tenant}/fleet/members/batch
func (h *LogisticsHandler) BatchInviteMembers(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	tenantSlug := chi.URLParam(r, "tenant")
	if claims, ok := authclient.ClaimsFromContext(r.Context()); ok && claims.GetTenantSlug() != "" {
		tenantSlug = claims.GetTenantSlug()
	}

	// Accepts {"members": [...]} (what the UIs send) or a bare array.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var requests []fleet.InviteByEmailRequest
	var wrapped struct {
		Members []fleet.InviteByEmailRequest `json:"members"`
	}
	if json.Unmarshal(body, &wrapped) == nil && wrapped.Members != nil {
		requests = wrapped.Members
	} else if err := json.Unmarshal(body, &requests); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(requests) == 0 {
		http.Error(w, "no members to invite", http.StatusBadRequest)
		return
	}

	if count, cerr := h.fleetSvc.CountActiveMembers(r.Context(), tenantID); cerr == nil {
		if !subscriptions.CheckStructuralLimit(w, r, "riders", subscriptions.LimitRiders, count+len(requests)-1) {
			return
		}
	}

	results := h.fleetSvc.BatchInviteByEmail(r.Context(), tenantID, tenantSlug, requests)
	respondJSON(w, http.StatusOK, results)
}

// CreateVehicle handles POST /api/v1/{tenant}/fleet/vehicles
func (h *LogisticsHandler) CreateVehicle(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req fleet.CreateVehicleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	v, err := h.fleetSvc.CreateVehicle(r.Context(), tenantID, uuid.Nil, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusCreated, signVehicleMedia(v))
}

// AssignVehicle handles POST /api/v1/{tenant}/fleet/members/{memberId}/vehicle
func (h *LogisticsHandler) AssignVehicle(w http.ResponseWriter, r *http.Request) {
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

	var body struct {
		VehicleID string `json:"vehicle_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	vehicleID, err := uuid.Parse(body.VehicleID)
	if err != nil {
		http.Error(w, "invalid vehicle id", http.StatusBadRequest)
		return
	}

	if err := h.fleetSvc.AssignVehicle(r.Context(), tenantID, memberID, vehicleID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "assigned"})
}

// UpdateVehicle handles PATCH /api/v1/{tenant}/fleet/vehicles/{vehicleId}
func (h *LogisticsHandler) UpdateVehicle(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	vehicleID, err := uuid.Parse(chi.URLParam(r, "vehicleId"))
	if err != nil {
		http.Error(w, "invalid vehicle id", http.StatusBadRequest)
		return
	}

	var req fleet.UpdateVehicleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	v, err := h.fleetSvc.UpdateVehicle(r.Context(), tenantID, vehicleID, req)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	respondJSON(w, http.StatusOK, signVehicleMedia(v))
}

// DeleteVehicle handles DELETE /api/v1/{tenant}/fleet/vehicles/{vehicleId}
func (h *LogisticsHandler) DeleteVehicle(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	vehicleID, err := uuid.Parse(chi.URLParam(r, "vehicleId"))
	if err != nil {
		http.Error(w, "invalid vehicle id", http.StatusBadRequest)
		return
	}

	if err := h.fleetSvc.DeleteVehicle(r.Context(), tenantID, vehicleID); err != nil {
		status := http.StatusConflict
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// DispatchTask handles POST /api/v1/{tenant}/tasks/{taskId}/dispatch
// Manually triggers the auto-dispatch algorithm for an unassigned task.
func (h *LogisticsHandler) DispatchTask(w http.ResponseWriter, r *http.Request) {
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

	if h.dispatcher == nil {
		http.Error(w, "dispatch not available", http.StatusServiceUnavailable)
		return
	}

	if err := h.dispatcher.DispatchTask(r.Context(), tenantID, taskID); err != nil {
		h.log.Warn("dispatch task", zap.String("task_id", taskID.String()), zap.Error(err))
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "dispatched"})
}

// GetPoD handles GET /api/v1/{tenant}/tasks/{taskId}/pod
// Returns the proof of delivery for a completed task.
func (h *LogisticsHandler) GetPoD(w http.ResponseWriter, r *http.Request) {
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

	if member := h.riderOnly(r, tenantID); member != nil {
		if t, terr := h.taskSvc.GetTask(r.Context(), tenantID, taskID); terr != nil || t.Status == "pending" || !h.riderMayView(r.Context(), t, member) {
			http.Error(w, "proof of delivery not found", http.StatusNotFound)
			return
		}
	}

	pod, err := h.taskSvc.Client().ProofOfDelivery.Query().
		Where(
			proofofdelivery.TaskID(taskID),
			proofofdelivery.TenantID(tenantID),
		).Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "proof of delivery not found", http.StatusNotFound)
			return
		}
		h.log.Error("get pod", zap.Error(err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, signPoDMedia(pod))
}

// --- helpers ---
// tenantIDFromClaims is now defined in tenant.go with platform-owner override support.
