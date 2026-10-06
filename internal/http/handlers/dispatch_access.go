package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	httpware "github.com/Bengo-Hub/httpware"
	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	"github.com/bengobox/logistics-service/internal/modules/dispatch"
	"github.com/bengobox/logistics-service/internal/modules/rbac"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

// Riders hold logistics.tasks.manage because it is what lets them move their own jobs along.
// That same permission guards the dispatcher's actions, so without a second check any rider
// could list every customer's delivery, assign jobs, cancel orders or record their own cash
// hand-in. A rider here is a fleet member of the tenant who does not also hold
// logistics.fleet.manage (an owner who also rides keeps full access).

// permissionChecker is the subset of the RBAC service used to tell dispatchers from riders.
type permissionChecker interface {
	HasPermission(ctx context.Context, tenantID, userID uuid.UUID, permissionCode string) (bool, error)
}

// SetPermissionChecker wires the RBAC service.
func (h *LogisticsHandler) SetPermissionChecker(p permissionChecker) { h.perms = p }

// SetTracker wires the rider location and ETA store used by the tracking endpoints.
func (h *LogisticsHandler) SetTracker(t *dispatch.AutoDispatcher) { h.tracker = t }

// riderOnly returns the caller's fleet membership when the caller is a rider without dispatch
// rights, else nil (dispatchers, admins, platform owners and service calls).
func (h *LogisticsHandler) riderOnly(r *http.Request, tenantID uuid.UUID) *ent.FleetMember {
	ctx := r.Context()
	if httpware.IsPlatformOwner(ctx) {
		return nil
	}
	claims, ok := authclient.ClaimsFromContext(ctx)
	if !ok || claims.Subject == "" {
		return nil
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil
	}
	member, err := h.taskSvc.Client().FleetMember.Query().
		Where(fleetmember.UserID(userID), fleetmember.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		return nil
	}
	if h.perms != nil {
		if can, perr := h.perms.HasPermission(ctx, tenantID, userID, rbac.PermFleetManage); perr == nil && can {
			return nil
		}
	}
	return member
}

// DispatcherOnly refuses riders on dispatcher routes (the dispatch board list, create, assign,
// unassign, cancel, auto-dispatch, rating, rider cash hand-ins).
func (h *LogisticsHandler) DispatcherOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := tenantIDFromClaims(r)
		if tenantID != uuid.Nil && h.riderOnly(r, tenantID) != nil {
			http.Error(w, "this action is for dispatchers; riders use their own jobs list", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// riderMayView reports whether a rider may read a task: one they hold or held, or an open job
// they could claim.
func (h *LogisticsHandler) riderMayView(ctx context.Context, t *ent.Task, member *ent.FleetMember) bool {
	if t.Status == "pending" {
		return true
	}
	held, err := h.taskSvc.Client().TaskAssignment.Query().
		Where(taskassignment.TaskID(t.ID), taskassignment.FleetMemberID(member.ID)).
		Exist(ctx)
	return err == nil && held
}

type reasonBody struct {
	Reason string `json:"reason"`
}

func decodeReason(r *http.Request) string {
	var b reasonBody
	_ = json.NewDecoder(r.Body).Decode(&b)
	return strings.TrimSpace(b.Reason)
}

// writeTaskError maps lifecycle errors to HTTP statuses.
func writeTaskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tasks.ErrNotYourTask), errors.Is(err, tasks.ErrRiderMayNotSetStatus):
		http.Error(w, strings.TrimPrefix(err.Error(), "tasks: "), http.StatusForbidden)
	case errors.Is(err, tasks.ErrTaskChanged), errors.Is(err, tasks.ErrTaskTaken), errors.Is(err, tasks.ErrIntakeBusy):
		http.Error(w, strings.TrimPrefix(err.Error(), "tasks: "), http.StatusConflict)
	case errors.Is(err, tasks.ErrTaskClosed), errors.Is(err, tasks.ErrAlreadyPickedUp):
		http.Error(w, strings.TrimPrefix(err.Error(), "tasks: "), http.StatusConflict)
	case err != nil && strings.Contains(err.Error(), "not found"):
		http.Error(w, "task not found", http.StatusNotFound)
	default:
		http.Error(w, strings.TrimPrefix(err.Error(), "tasks: "), http.StatusBadRequest)
	}
}

// DeclineJob handles POST /api/v1/{tenant}/riders/me/tasks/{taskId}/decline {reason}
// The rider hands a job back before collecting the order. It returns to the open pool and, with
// auto-assign on, goes to the next nearest rider (never back to this one).
func (h *LogisticsHandler) DeclineJob(w http.ResponseWriter, r *http.Request) {
	tenantID, member, ok := h.currentRider(w, r)
	if !ok {
		return
	}
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}
	t, err := h.taskSvc.DeclineTask(r.Context(), tenantID, taskID, member.ID, decodeReason(r))
	if err != nil {
		writeTaskError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, toTaskResponse(t))
}

// UnassignTask handles POST /api/v1/{tenant}/tasks/{taskId}/unassign {reason}
// A dispatcher takes a job back from its rider before pickup.
func (h *LogisticsHandler) UnassignTask(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if tenantID == uuid.Nil || err != nil {
		http.Error(w, "invalid task", http.StatusBadRequest)
		return
	}
	t, err := h.taskSvc.UnassignTask(r.Context(), tenantID, taskID, decodeReason(r), h.actor(r, "dispatcher"))
	if err != nil {
		writeTaskError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, toTaskResponse(t))
}

// CancelTask handles POST /api/v1/{tenant}/tasks/{taskId}/cancel {reason}
// A dispatcher (or ordering, for a cancelled order) closes a delivery that will not happen.
func (h *LogisticsHandler) CancelTask(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if tenantID == uuid.Nil || err != nil {
		http.Error(w, "invalid task", http.StatusBadRequest)
		return
	}
	t, err := h.taskSvc.CancelTask(r.Context(), tenantID, taskID, decodeReason(r), h.actor(r, "dispatcher"))
	if err != nil {
		writeTaskError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, toTaskResponse(t))
}

// actor builds the history actor for the calling user.
func (h *LogisticsHandler) actor(r *http.Request, kind string) tasks.Actor {
	a := tasks.Actor{Type: kind}
	if claims, ok := authclient.ClaimsFromContext(r.Context()); ok {
		if id, err := uuid.Parse(claims.Subject); err == nil {
			a.ID = id
		}
	}
	return a
}

// TrackingResponse is the live view of one delivery: where the rider is and how long until the
// order arrives. Field names match ordering-backend's logistics.TrackingInfo.
type TrackingResponse struct {
	TaskID        uuid.UUID          `json:"task_id"`
	Status        string             `json:"status"`
	RiderLocation *TrackingRiderInfo `json:"rider_location,omitempty"`
	ETAMinutes    int                `json:"eta_minutes,omitempty"`
	ETAAt         *time.Time         `json:"eta_at,omitempty"`
	DistanceKm    float64            `json:"distance_km,omitempty"`
	RiderName     string             `json:"rider_name,omitempty"`
	RiderPhone    string             `json:"rider_phone,omitempty"`
	LastUpdatedAt time.Time          `json:"last_updated_at"`
}

// TrackingRiderInfo is the rider's last reported position.
type TrackingRiderInfo struct {
	RiderID   string    `json:"rider_id"`
	Latitude  float64   `json:"latitude"`
	Longitude float64   `json:"longitude"`
	UpdatedAt time.Time `json:"updated_at"`
}

// buildTracking assembles the tracking view for a task of tenantID. The rider's position is only
// shared while the rider is working the task, never after it closes.
func (h *LogisticsHandler) buildTracking(ctx context.Context, tenantID, taskID uuid.UUID) (*TrackingResponse, error) {
	t, err := h.taskSvc.Client().Task.Query().
		Where(task.ID(taskID), task.TenantID(tenantID)).
		WithAssignments(func(q *ent.TaskAssignmentQuery) {
			q.Where(taskassignment.StatusIn("assigned", "accepted")).WithMember(func(mq *ent.FleetMemberQuery) {
				mq.WithUser()
			})
		}).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	resp := &TrackingResponse{TaskID: t.ID, Status: t.Status, LastUpdatedAt: t.UpdatedAt}
	if len(t.Edges.Assignments) == 0 || !tasks.IsInProgress(t.Status) {
		return resp, nil
	}
	a := t.Edges.Assignments[0]
	if m := a.Edges.Member; m != nil && m.Edges.User != nil {
		resp.RiderName = m.Edges.User.FullName
		resp.RiderPhone = m.Edges.User.Phone
	}
	if h.tracker != nil {
		if loc, lerr := h.tracker.GetRiderLocation(ctx, tenantID, a.FleetMemberID); lerr == nil {
			resp.RiderLocation = &TrackingRiderInfo{
				RiderID: a.FleetMemberID.String(), Latitude: loc.Latitude, Longitude: loc.Longitude,
				UpdatedAt: loc.SeenAt,
			}
			if loc.SeenAt.After(resp.LastUpdatedAt) {
				resp.LastUpdatedAt = loc.SeenAt
			}
		}
		if eta, ok := h.tracker.LastETA(ctx, taskID); ok {
			resp.ETAMinutes = int(eta.Minutes + 0.5)
			resp.DistanceKm = eta.DistanceKm
			at := eta.At.Add(time.Duration(eta.Minutes * float64(time.Minute)))
			resp.ETAAt = &at
		}
	}
	return resp, nil
}

// GetTaskTracking handles GET /api/v1/{tenant}/tasks/{taskId}/tracking (dispatchers, and the
// rider holding the task).
func (h *LogisticsHandler) GetTaskTracking(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	taskID, err := uuid.Parse(chi.URLParam(r, "taskId"))
	if tenantID == uuid.Nil || err != nil {
		http.Error(w, "invalid task", http.StatusBadRequest)
		return
	}
	if member := h.riderOnly(r, tenantID); member != nil {
		if holder, held := h.taskSvc.ActiveAssignee(r.Context(), taskID); !held || holder != member.ID {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
	}
	h.writeTracking(w, r, tenantID, taskID)
}

// S2SGetTaskTracking handles GET /api/v1/s2s/dispatch/{tenant}/tasks/{taskId}/tracking
// (service key), for ordering's customer order tracker.
func (h *LogisticsHandler) S2SGetTaskTracking(w http.ResponseWriter, r *http.Request) {
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
	h.writeTracking(w, r, tenantID, taskID)
}

func (h *LogisticsHandler) writeTracking(w http.ResponseWriter, r *http.Request, tenantID, taskID uuid.UUID) {
	resp, err := h.buildTracking(r.Context(), tenantID, taskID)
	if err != nil {
		if ent.IsNotFound(err) {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		h.log.Error("task tracking", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, resp)
}
