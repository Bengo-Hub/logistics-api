package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/shifts"
)

// ShiftHandler serves the dispatcher's shift schedule. Riders go online and offline through
// /riders/me/shift (LogisticsHandler), which uses the same shifts.Service.
type ShiftHandler struct {
	svc    *shifts.Service
	logger *zap.Logger
	view   func(http.Handler) http.Handler
	manage func(http.Handler) http.Handler
}

// NewShiftHandler creates a new ShiftHandler.
func NewShiftHandler(svc *shifts.Service, logger *zap.Logger) *ShiftHandler {
	return &ShiftHandler{svc: svc, logger: logger.Named("shifts")}
}

// SetGates sets the permission middleware: view for reads, manage for changes.
func (h *ShiftHandler) SetGates(view, manage func(http.Handler) http.Handler) {
	h.view, h.manage = view, manage
}

func gate(mw func(http.Handler) http.Handler) []func(http.Handler) http.Handler {
	if mw == nil {
		return nil
	}
	return []func(http.Handler) http.Handler{mw}
}

func (h *ShiftHandler) RegisterRoutes(r chi.Router) {
	r.Route("/shifts", func(s chi.Router) {
		s.With(gate(h.view)...).Get("/", h.ListShifts)
		s.With(gate(h.view)...).Get("/{shiftId}", h.GetShift)
		s.With(gate(h.manage)...).Post("/", h.CreateShift)
		s.With(gate(h.manage)...).Patch("/{shiftId}/status", h.UpdateShiftStatus)
		s.With(gate(h.manage)...).Post("/{shiftId}/start", h.StartShift)
		s.With(gate(h.manage)...).Post("/{shiftId}/end", h.EndShift)
		s.With(gate(h.manage)...).Post("/{shiftId}/cancel", h.CancelShift)
		s.With(gate(h.manage)...).Delete("/{shiftId}", h.DeleteShift)
	})
}

// shiftError maps service errors to responses with a message the UI can show.
func (h *ShiftHandler) shiftError(w http.ResponseWriter, err error, op string) {
	switch {
	case errors.Is(err, shifts.ErrNotFound), errors.Is(err, shifts.ErrRiderNotFound):
		respondJSON(w, http.StatusNotFound, map[string]string{"error": shiftMessage(err)})
	case errors.Is(err, shifts.ErrInvalid), errors.Is(err, shifts.ErrRiderInactive):
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": shiftMessage(err)})
	case errors.Is(err, shifts.ErrOverlap), errors.Is(err, shifts.ErrTransition):
		respondJSON(w, http.StatusConflict, map[string]string{"error": shiftMessage(err)})
	default:
		h.logger.Error(op, zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "something went wrong with the shift"})
	}
}

func shiftMessage(err error) string {
	switch {
	case errors.Is(err, shifts.ErrNotFound):
		return "Shift not found."
	case errors.Is(err, shifts.ErrRiderNotFound):
		return "Rider not found in this business."
	case errors.Is(err, shifts.ErrRiderInactive):
		return "This rider is not active. Approve or reinstate them first."
	case errors.Is(err, shifts.ErrOverlap):
		return "This rider already has a shift in that time."
	}
	// ErrInvalid and ErrTransition carry the detail after the sentinel text.
	return err.Error()
}

func parseShiftID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "shiftId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shift ID"})
		return uuid.Nil, false
	}
	return id, true
}

// ListShifts returns the tenant's shifts.
// GET /api/v1/{tenant}/shifts?status=&fleet_member_id=&from=&to=&limit=&offset=
// from and to are RFC3339; they select shifts overlapping that window.
func (h *ShiftHandler) ListShifts(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	q := r.URL.Query()
	f := shifts.ListFilter{Status: q.Get("status")}
	if v := q.Get("fleet_member_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			f.MemberID = id
		}
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &t
		}
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))

	list, total, err := h.svc.List(r.Context(), tenantID, f)
	if err != nil {
		h.shiftError(w, err, "list shifts")
		return
	}
	respondJSON(w, http.StatusOK, map[string]any{"data": list, "total": total})
}

// GetShift returns one shift.
// GET /api/v1/{tenant}/shifts/{shiftId}
func (h *ShiftHandler) GetShift(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	id, ok := parseShiftID(w, r)
	if !ok {
		return
	}
	s, err := h.svc.Get(r.Context(), tenantID, id)
	if err != nil {
		h.shiftError(w, err, "get shift")
		return
	}
	respondJSON(w, http.StatusOK, s)
}

type createShiftReq struct {
	FleetMemberID string   `json:"fleet_member_id"`
	ShiftStart    string   `json:"shift_start"`
	ShiftEnd      string   `json:"shift_end"`
	ZoneIDs       []string `json:"zone_ids,omitempty"`
}

// CreateShift schedules a shift. Times are RFC3339.
// POST /api/v1/{tenant}/shifts
func (h *ShiftHandler) CreateShift(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	var req createShiftReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	memberID, err := uuid.Parse(req.FleetMemberID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "Choose a rider."})
		return
	}
	start, err := time.Parse(time.RFC3339, req.ShiftStart)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shift_start, use RFC3339"})
		return
	}
	end, err := time.Parse(time.RFC3339, req.ShiftEnd)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shift_end, use RFC3339"})
		return
	}
	var zones []uuid.UUID
	for _, z := range req.ZoneIDs {
		if id, e := uuid.Parse(z); e == nil {
			zones = append(zones, id)
		}
	}
	s, err := h.svc.Create(r.Context(), tenantID, shifts.CreateRequest{
		FleetMemberID: memberID, Start: start, End: end, ZoneIDs: zones,
	})
	if err != nil {
		h.shiftError(w, err, "create shift")
		return
	}
	respondJSON(w, http.StatusCreated, s)
}

// UpdateShiftStatus moves a shift to a status the state machine allows
// (scheduled to active or cancelled, active to completed).
// PATCH /api/v1/{tenant}/shifts/{shiftId}/status
func (h *ShiftHandler) UpdateShiftStatus(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	id, ok := parseShiftID(w, r)
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "status is required"})
		return
	}
	s, err := h.svc.SetStatus(r.Context(), tenantID, id, req.Status)
	if err != nil {
		h.shiftError(w, err, "update shift status")
		return
	}
	respondJSON(w, http.StatusOK, s)
}

// StartShift makes a scheduled shift active.
// POST /api/v1/{tenant}/shifts/{shiftId}/start
func (h *ShiftHandler) StartShift(w http.ResponseWriter, r *http.Request) {
	h.apply(w, r, "start shift", h.svc.Start)
}

// EndShift completes an active shift.
// POST /api/v1/{tenant}/shifts/{shiftId}/end
func (h *ShiftHandler) EndShift(w http.ResponseWriter, r *http.Request) {
	h.apply(w, r, "end shift", h.svc.End)
}

// CancelShift cancels a scheduled shift.
// POST /api/v1/{tenant}/shifts/{shiftId}/cancel
func (h *ShiftHandler) CancelShift(w http.ResponseWriter, r *http.Request) {
	h.apply(w, r, "cancel shift", h.svc.Cancel)
}

func (h *ShiftHandler) apply(w http.ResponseWriter, r *http.Request, op string,
	fn func(ctx context.Context, tenantID, id uuid.UUID) (*shifts.Shift, error)) {
	tenantID := tenantIDFromClaims(r)
	id, ok := parseShiftID(w, r)
	if !ok {
		return
	}
	s, err := fn(r.Context(), tenantID, id)
	if err != nil {
		h.shiftError(w, err, op)
		return
	}
	respondJSON(w, http.StatusOK, s)
}

// DeleteShift removes a shift that never ran.
// DELETE /api/v1/{tenant}/shifts/{shiftId}
func (h *ShiftHandler) DeleteShift(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	id, ok := parseShiftID(w, r)
	if !ok {
		return
	}
	if err := h.svc.Delete(r.Context(), tenantID, id); err != nil {
		h.shiftError(w, err, "delete shift")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
