package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/userroleassignment"
	"github.com/bengobox/logistics-service/internal/modules/rbac"
)

// RBACHandler serves the Roles page: roles with their permissions, the permission
// catalogue, and who holds which role.
type RBACHandler struct {
	logger      *zap.Logger
	rbacService *rbac.Service
	client      *ent.Client
}

// NewRBACHandler creates a new RBAC handler.
func NewRBACHandler(logger *zap.Logger, rbacService *rbac.Service, client *ent.Client) *RBACHandler {
	return &RBACHandler{logger: logger, rbacService: rbacService, client: client}
}

// AssignRoleRequest represents a request to assign a role.
type AssignRoleRequest struct {
	UserID uuid.UUID `json:"user_id"`
	RoleID uuid.UUID `json:"role_id"`
}

// AssignRole handles POST /{tenant}/rbac/assignments {user_id, role_id}.
func (h *RBACHandler) AssignRole(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var req AssignRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == uuid.Nil || req.RoleID == uuid.Nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id and role_id are required"})
		return
	}
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	assignedBy, err := claims.UserID()
	if err != nil || assignedBy == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid user ID"})
		return
	}
	switch err := h.rbacService.AssignRole(r.Context(), tenantID, req.UserID, req.RoleID, assignedBy); {
	case errors.Is(err, rbac.ErrAlreadyAssigned):
		respondJSON(w, http.StatusConflict, map[string]string{"error": "the user already has this role"})
		return
	case errors.Is(err, rbac.ErrRoleNotInTenant):
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "role not found for this tenant"})
		return
	case err != nil:
		h.logger.Error("failed to assign role", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to assign role"})
		return
	}
	views, _ := rbac.ListAssignmentViews(r.Context(), h.client, tenantID, &req.UserID)
	respondJSON(w, http.StatusCreated, map[string]any{"assignments": views})
}

// RevokeRole handles DELETE /{tenant}/rbac/assignments/{id}.
func (h *RBACHandler) RevokeRole(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	assignmentID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid assignment ID"})
		return
	}
	a, err := h.client.UserRoleAssignment.Query().
		Where(userroleassignment.ID(assignmentID), userroleassignment.TenantID(tenantID)).
		Only(r.Context())
	if ent.IsNotFound(err) {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "assignment not found"})
		return
	}
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load assignment"})
		return
	}
	if err := h.rbacService.RevokeRole(r.Context(), tenantID, a.UserID, a.RoleID); err != nil {
		h.logger.Error("failed to revoke role", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to revoke role"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"message": "role revoked"})
}

// ListAssignments handles GET /{tenant}/rbac/assignments[?user_id=].
func (h *RBACHandler) ListAssignments(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	var userID *uuid.UUID
	if raw := r.URL.Query().Get("user_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user_id"})
			return
		}
		userID = &id
	}
	views, err := rbac.ListAssignmentViews(r.Context(), h.client, tenantID, userID)
	if err != nil {
		h.logger.Error("failed to list assignments", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list assignments"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"assignments": views})
}

// ListRoles handles GET /{tenant}/rbac/roles: roles with permission codes and holder counts.
func (h *RBACHandler) ListRoles(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	// A tenant opening the page before the seed reached it still sees its system roles.
	if err := h.rbacService.EnsureSystemRoles(r.Context(), tenantID); err != nil {
		h.logger.Warn("ensure system roles", zap.Error(err))
	}
	views, err := rbac.ListRoleViews(r.Context(), h.client, tenantID)
	if err != nil {
		h.logger.Error("failed to list roles", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list roles"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"roles": views})
}

// ListPermissions handles GET /{tenant}/rbac/permissions.
func (h *RBACHandler) ListPermissions(w http.ResponseWriter, r *http.Request) {
	views, err := rbac.ListPermissionViews(r.Context(), h.client)
	if err != nil {
		h.logger.Error("failed to list permissions", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list permissions"})
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"permissions": views})
}

// RegisterRoutes registers RBAC routes on the given tenant-scoped router. manage guards
// everything that reveals or changes who holds which role; without it any signed-in user
// could make themselves admin.
func (h *RBACHandler) RegisterRoutes(r chi.Router, manage func(http.Handler) http.Handler) {
	r.With(manage).Post("/rbac/assignments", h.AssignRole)
	r.With(manage).Get("/rbac/assignments", h.ListAssignments)
	r.With(manage).Delete("/rbac/assignments/{id}", h.RevokeRole)
	r.Get("/rbac/roles", h.ListRoles)
	r.Get("/rbac/permissions", h.ListPermissions)
}
