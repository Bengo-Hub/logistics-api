package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

// currentRider resolves the calling rider's fleet membership from the JWT subject (auth user ID)
// and tenant. On failure it writes the response and returns ok=false.
func (h *LogisticsHandler) currentRider(w http.ResponseWriter, r *http.Request) (uuid.UUID, *ent.FleetMember, bool) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return uuid.Nil, nil, false
	}
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok || claims.Subject == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return uuid.Nil, nil, false
	}
	authUserID, err := uuid.Parse(claims.Subject)
	if err != nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return uuid.Nil, nil, false
	}
	member, err := h.taskSvc.Client().FleetMember.Query().
		Where(fleetmember.UserID(authUserID), fleetmember.TenantID(tenantID)).
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "rider not found in fleet", http.StatusNotFound)
			return uuid.Nil, nil, false
		}
		h.log.Error("resolve fleet member", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return uuid.Nil, nil, false
	}
	return tenantID, member, true
}

// ListOpenJobs handles GET /api/v1/{tenant}/riders/me/open-tasks
// Delivery jobs no rider holds yet, oldest first, for riders to pick up themselves. Returns an
// empty list with claim_enabled=false when the business assigns every job from dispatch.
func (h *LogisticsHandler) ListOpenJobs(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := h.currentRider(w, r)
	if !ok {
		return
	}
	if !h.taskSvc.RiderSelfClaimEnabled(r.Context(), tenantID) {
		respondJSON(w, http.StatusOK, map[string]any{"data": []any{}, "claim_enabled": false})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := h.taskSvc.ListOpenTasks(r.Context(), tenantID, limit)
	if err != nil {
		h.log.Error("list open jobs", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"data": toTaskResponses(list), "claim_enabled": true})
}

// ClaimJob handles POST /api/v1/{tenant}/riders/me/tasks/{taskId}/claim
// The rider takes an open job; it becomes theirs and counts as accepted. 409 when someone else
// got it first, 403 when the business assigns jobs itself or the rider is not active.
func (h *LogisticsHandler) ClaimJob(w http.ResponseWriter, r *http.Request) {
	tenantID, member, ok := h.currentRider(w, r)
	if !ok {
		return
	}
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}
	claimed, err := h.taskSvc.ClaimTask(r.Context(), tenantID, taskID, member.ID)
	switch {
	case errors.Is(err, tasks.ErrTaskTaken):
		http.Error(w, "Another rider has already taken this job.", http.StatusConflict)
		return
	case errors.Is(err, tasks.ErrSelfClaimDisabled):
		http.Error(w, "Jobs are assigned by your dispatcher.", http.StatusForbidden)
		return
	case errors.Is(err, tasks.ErrRiderNotActive):
		http.Error(w, "Your rider account is not active yet.", http.StatusForbidden)
		return
	case err != nil:
		h.log.Error("claim job", zap.String("task_id", taskID.String()), zap.Error(err))
		http.Error(w, "could not take this job", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, toTaskResponse(claimed))
}
