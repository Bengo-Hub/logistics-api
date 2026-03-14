package handlers

import (
	"encoding/json"
	"net/http"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/bengobox/logistics-service/internal/modules/identity"
	"github.com/google/uuid"
)

type IdentityHandler struct {
	svc *identity.Service
}

func NewIdentityHandler(svc *identity.Service) *IdentityHandler {
	return &IdentityHandler{svc: svc}
}

func (h *IdentityHandler) GetMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	authID, _ := uuid.Parse(claims.Subject)
	
	u, err := h.svc.GetRiderProfile(r.Context(), authID)
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
		resp["rider"] = fm
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

	var req identity.UpdateRiderProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	u, err := h.svc.UpdateRiderProfile(r.Context(), authID, req)
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
		resp["rider"] = fm
		if fm.Edges.Vehicle != nil {
			resp["vehicle"] = fm.Edges.Vehicle
		}
	} else {
		resp["status"] = "none"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
