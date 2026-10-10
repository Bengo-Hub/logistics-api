package handlers

import (
	"encoding/json"
	"github.com/Bengo-Hub/httpware"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/bengobox/logistics-service/internal/modules/fleet"
	"github.com/bengobox/logistics-service/internal/modules/identity"
	"github.com/bengobox/logistics-service/internal/modules/rbac"
	"github.com/google/uuid"
)

type IdentityHandler struct {
	svc     *identity.Service
	rbacSvc *rbac.Service
}

func NewIdentityHandler(svc *identity.Service, rbacSvc *rbac.Service) *IdentityHandler {
	return &IdentityHandler{svc: svc, rbacSvc: rbacSvc}
}

// GetAuthMe returns the caller's service-level role and permissions (Trinity Layer 3 enrichment).
// Frontends call this after SSO /auth/me to get logistics-specific RBAC data.
func (h *IdentityHandler) GetAuthMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	authID, _ := uuid.Parse(claims.Subject)
	tenantID, _ := uuid.Parse(claims.TenantID)

	type roleInfo struct {
		ID   string `json:"id"`
		Code string `json:"code"`
		Name string `json:"name"`
	}

	var serviceRole *roleInfo
	var permCodes []string

	if h.rbacSvc != nil && tenantID != uuid.Nil {
		codes, full, err := h.rbacSvc.EffectivePermissions(r.Context(), tenantID, authID)
		switch {
		case err != nil:
		case full:
			// Same rule as RequirePermission: admins pass every check.
			if all, aerr := h.rbacSvc.AllPermissionCodes(r.Context()); aerr == nil {
				permCodes = all
			}
			serviceRole = &roleInfo{Code: rbac.RoleAdmin, Name: "Administrator"}
		default:
			permCodes = codes
			if roles, rerr := h.rbacSvc.GetUserRoles(r.Context(), tenantID, authID); rerr == nil && len(roles) > 0 {
				serviceRole = &roleInfo{ID: roles[0].ID.String(), Code: roles[0].RoleCode, Name: roles[0].Name}
			} else if mapped := rbac.SystemRolesForSSO(claims.Roles); len(mapped) > 0 {
				serviceRole = &roleInfo{Code: mapped[0], Name: mapped[0]}
			}
		}
	}
	if permCodes == nil {
		permCodes = []string{}
	}

	// Resolve enabled modules and use_case for this tenant (Trinity Layer 3 module gating).
	// Platform owners get all modules; others get tenant-specific or use_case defaults.
	var enabledModules []string
	useCase := ""
	if claims.IsPlatformOwner {
		enabledModules = nil // nil = all modules (frontend treats nil/empty as unrestricted)
	} else if h.svc != nil && tenantID != uuid.Nil {
		enabledModules, useCase = h.svc.ResolveEnabledModules(r.Context(), tenantID)
	}

	resp := map[string]any{
		"id":                claims.Subject,
		"email":             claims.Email,
		"global_roles":      claims.Roles,
		"service_role":      serviceRole,
		"permissions":       permCodes,
		"tenant_id":         claims.TenantID,
		"tenant_slug":       claims.GetTenantSlug(),
		"is_platform_owner": claims.IsPlatformOwner,
		"use_case":          useCase,
		"enabled_modules":   enabledModules,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *IdentityHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	authID, _ := uuid.Parse(claims.Subject)
	tenantID, _ := uuid.Parse(claims.TenantID)

	u, err := h.svc.GetRiderProfile(r.Context(), authID, tenantID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Combine data for the rider response
	resp := map[string]any{
		"user": u,
	}

	if len(u.Edges.FleetMemberships) > 0 {
		fm := u.Edges.FleetMemberships[0]
		resp["status"] = fm.Status
		// freelance riders go through KYC; staff riders (HR-vetted employees) do not.
		resp["employment"] = fleet.EmploymentOf(fm).Type
		resp["rider"] = signFleetMemberMedia(fm)
		if fm.Edges.Vehicle != nil {
			resp["vehicle"] = fm.Edges.Vehicle
		}
	} else {
		resp["status"] = "none"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *IdentityHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	authID, _ := uuid.Parse(claims.Subject)
	tenantID, _ := uuid.Parse(claims.TenantID)

	var req identity.UpdateRiderProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}
	// The form re-submits the signed URLs it was given; store the plain form.
	for _, u := range []*string{&req.IDPassportAttachment, &req.RiderPhoto, &req.ImageLicensePlate, &req.ImageSideView} {
		*u = httpware.StripMediaSignature(*u)
	}

	u, err := h.svc.UpdateRiderProfile(r.Context(), authID, tenantID, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Combine data for the rider response
	resp := map[string]any{
		"user": u,
	}

	if len(u.Edges.FleetMemberships) > 0 {
		fm := u.Edges.FleetMemberships[0]
		resp["status"] = fm.Status
		// freelance riders go through KYC; staff riders (HR-vetted employees) do not.
		resp["employment"] = fleet.EmploymentOf(fm).Type
		resp["rider"] = signFleetMemberMedia(fm)
		if fm.Edges.Vehicle != nil {
			resp["vehicle"] = fm.Edges.Vehicle
		}
	} else {
		resp["status"] = "none"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
