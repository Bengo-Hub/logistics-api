package handlers

import (
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/shifts"
)

// SetShifts wires the shift service for the rider's online and offline switch.
func (h *LogisticsHandler) SetShifts(s *shifts.Service) { h.shifts = s }

// riderShiftResponse is the rider app's view: online when a shift is active.
type riderShiftResponse struct {
	Online bool          `json:"online"`
	Shift  *shifts.Shift `json:"shift"`
	Next   *shifts.Shift `json:"next_shift"`
}

// GetMyShift handles GET /api/v1/{tenant}/riders/me/shift
func (h *LogisticsHandler) GetMyShift(w http.ResponseWriter, r *http.Request) {
	tenantID, member, ok := h.currentRider(w, r)
	if !ok {
		return
	}
	if h.shifts == nil {
		respondJSON(w, http.StatusOK, riderShiftResponse{})
		return
	}
	active, next, err := h.shifts.Current(r.Context(), tenantID, member.ID)
	if err != nil {
		h.log.Error("rider shift", zap.Error(err))
		http.Error(w, "could not load your shift", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, riderShiftResponse{Online: active != nil, Shift: active, Next: next})
}

// GoOnline handles POST /api/v1/{tenant}/riders/me/shift/online: starts the rider's scheduled
// shift, or an open-ended one when nothing is scheduled. Jobs are offered once the app sends
// the rider's location.
func (h *LogisticsHandler) GoOnline(w http.ResponseWriter, r *http.Request) {
	tenantID, member, ok := h.currentRider(w, r)
	if !ok {
		return
	}
	if h.shifts == nil {
		http.Error(w, "shifts are not available", http.StatusServiceUnavailable)
		return
	}
	active, err := h.shifts.GoOnline(r.Context(), tenantID, member.ID)
	if errors.Is(err, shifts.ErrRiderInactive) {
		respondJSON(w, http.StatusForbidden, map[string]string{
			"code":  "rider_not_active",
			"error": "Your account is not active yet. The business has to approve you before you can go online.",
		})
		return
	}
	if err != nil {
		h.log.Error("go online", zap.Error(err))
		http.Error(w, "could not go online", http.StatusInternalServerError)
		return
	}
	_, next, _ := h.shifts.Current(r.Context(), tenantID, member.ID)
	respondJSON(w, http.StatusOK, riderShiftResponse{Online: true, Shift: active, Next: next})
}

// GoOffline handles POST /api/v1/{tenant}/riders/me/shift/offline: ends the active shift now and
// takes the rider out of auto-dispatch at once. Jobs the rider already holds stay theirs.
func (h *LogisticsHandler) GoOffline(w http.ResponseWriter, r *http.Request) {
	tenantID, member, ok := h.currentRider(w, r)
	if !ok {
		return
	}
	if h.shifts == nil {
		http.Error(w, "shifts are not available", http.StatusServiceUnavailable)
		return
	}
	if _, err := h.shifts.GoOffline(r.Context(), tenantID, member.ID); err != nil {
		h.log.Error("go offline", zap.Error(err))
		http.Error(w, "could not go offline", http.StatusInternalServerError)
		return
	}
	if h.tracker != nil {
		if err := h.tracker.MarkOffline(r.Context(), tenantID, member.ID); err != nil {
			h.log.Warn("drop offline rider from dispatch", zap.Error(err))
		}
	}
	_, next, _ := h.shifts.Current(r.Context(), tenantID, member.ID)
	respondJSON(w, http.StatusOK, riderShiftResponse{Online: false, Next: next})
}
