package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	authclient "github.com/Bengo-Hub/shared-auth-client"

	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

// GetMyCash handles GET /api/v1/{tenant}/riders/me/cash
// The cash-on-delivery money this rider holds and must hand in at the outlet, per delivery.
func (h *LogisticsHandler) GetMyCash(w http.ResponseWriter, r *http.Request) {
	tenantID, member, ok := h.currentRider(w, r)
	if !ok {
		return
	}
	held, err := h.taskSvc.RiderCashHeld(r.Context(), tenantID, member.ID)
	if err != nil {
		h.log.Error("rider cash held", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, held)
}

// ListCashWithRiders handles GET /api/v1/{tenant}/cash/riders
// Every rider still holding delivery cash, most first (dispatcher / outlet manager).
func (h *LogisticsHandler) ListCashWithRiders(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	list, err := h.taskSvc.CashWithRiders(r.Context(), tenantID)
	if err != nil {
		h.log.Error("cash with riders", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	total := 0.0
	for _, rc := range list {
		total += rc.Held
	}
	respondJSON(w, http.StatusOK, map[string]any{"data": list, "total_held": total})
}

type remitRequest struct {
	// AmountReceived is the cash the rider actually handed over (counted at the outlet).
	AmountReceived float64 `json:"amount_received"`
	Notes          string  `json:"notes"`
}

// RecordCashRemittance handles POST /api/v1/{tenant}/cash/riders/{memberId}/remit
// The rider handed in their delivery cash. Every outstanding cash delivery is closed with one
// remittance; a shortfall is returned and kept on the deliveries.
func (h *LogisticsHandler) RecordCashRemittance(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	memberID, err := uuid.Parse(chi.URLParam(r, "memberId"))
	if err != nil {
		http.Error(w, "invalid rider id", http.StatusBadRequest)
		return
	}
	var req remitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	receivedBy := ""
	if claims, ok := authclient.ClaimsFromContext(r.Context()); ok {
		receivedBy = claims.Email
		if receivedBy == "" {
			receivedBy = claims.Subject
		}
	}
	rem, err := h.taskSvc.RecordRemittance(r.Context(), tenantID, memberID, req.AmountReceived, receivedBy, req.Notes)
	switch {
	case errors.Is(err, tasks.ErrNothingToRemit):
		http.Error(w, "This rider has no delivery cash to hand in.", http.StatusConflict)
		return
	case err != nil:
		h.log.Error("record cash remittance", zap.String("member_id", memberID.String()), zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	respondJSON(w, http.StatusOK, rem)
}
