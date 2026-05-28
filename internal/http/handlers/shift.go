package handlers

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/ridershift"
)

// ShiftHandler handles rider shift operations.
type ShiftHandler struct {
	client *ent.Client
	logger *zap.Logger
}

// NewShiftHandler creates a new ShiftHandler.
func NewShiftHandler(client *ent.Client, logger *zap.Logger) *ShiftHandler {
	return &ShiftHandler{client: client, logger: logger.Named("shifts")}
}

func (h *ShiftHandler) RegisterRoutes(r chi.Router) {
	r.Route("/shifts", func(s chi.Router) {
		s.Get("/", h.ListShifts)
		s.Post("/", h.CreateShift)
		s.Get("/{shiftId}", h.GetShift)
		s.Patch("/{shiftId}/status", h.UpdateShiftStatus)
		s.Post("/{shiftId}/start", h.StartShift)
		s.Post("/{shiftId}/end", h.EndShift)
		s.Delete("/{shiftId}", h.DeleteShift)
	})
}

// ListShifts returns shifts for a tenant with optional filters.
// GET /api/v1/{tenant}/shifts
func (h *ShiftHandler) ListShifts(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant"})
		return
	}

	q := h.client.RiderShift.Query().
		Where(ridershift.TenantID(tenantID))

	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(ridershift.StatusEQ(ridershift.Status(s)))
	}
	if fm := r.URL.Query().Get("fleet_member_id"); fm != "" {
		if fmID, err := uuid.Parse(fm); err == nil {
			q = q.Where(ridershift.FleetMemberID(fmID))
		}
	}

	shifts, err := q.Order(ent.Desc(ridershift.FieldShiftStart)).Limit(100).All(r.Context())
	if err != nil {
		h.logger.Error("list shifts", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list shifts"})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"data": shifts, "total": len(shifts)})
}

type createShiftReq struct {
	FleetMemberID string    `json:"fleet_member_id"`
	ShiftStart    string    `json:"shift_start"`
	ShiftEnd      string    `json:"shift_end"`
	ZoneIDs       []string  `json:"zone_ids,omitempty"`
}

// CreateShift creates a new rider shift.
// POST /api/v1/{tenant}/shifts
func (h *ShiftHandler) CreateShift(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid tenant"})
		return
	}

	var req createShiftReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	fmID, err := uuid.Parse(req.FleetMemberID)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid fleet_member_id"})
		return
	}

	start, err := time.Parse(time.RFC3339, req.ShiftStart)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shift_start, use RFC3339 format"})
		return
	}

	end, err := time.Parse(time.RFC3339, req.ShiftEnd)
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shift_end, use RFC3339 format"})
		return
	}

	create := h.client.RiderShift.Create().
		SetTenantID(tenantID).
		SetFleetMemberID(fmID).
		SetShiftStart(start).
		SetShiftEnd(end)

	if len(req.ZoneIDs) > 0 {
		var zoneUUIDs []uuid.UUID
		for _, z := range req.ZoneIDs {
			if id, e := uuid.Parse(z); e == nil {
				zoneUUIDs = append(zoneUUIDs, id)
			}
		}
		if len(zoneUUIDs) > 0 {
			create = create.SetZoneIds(zoneUUIDs)
		}
	}

	shift, err := create.Save(r.Context())
	if err != nil {
		h.logger.Error("create shift", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create shift"})
		return
	}

	respondJSON(w, http.StatusCreated, shift)
}

// GetShift returns a single shift.
// GET /api/v1/{tenant}/shifts/{shiftId}
func (h *ShiftHandler) GetShift(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(chi.URLParam(r, "shiftId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shift ID"})
		return
	}

	shift, err := h.client.RiderShift.Get(r.Context(), sid)
	if err != nil {
		if ent.IsNotFound(err) {
			respondJSON(w, http.StatusNotFound, map[string]string{"error": "shift not found"})
			return
		}
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get shift"})
		return
	}

	respondJSON(w, http.StatusOK, shift)
}

// UpdateShiftStatus updates a shift's status.
// PATCH /api/v1/{tenant}/shifts/{shiftId}/status
func (h *ShiftHandler) UpdateShiftStatus(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(chi.URLParam(r, "shiftId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shift ID"})
		return
	}

	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "status is required"})
		return
	}

	shift, err := h.client.RiderShift.UpdateOneID(sid).
		SetStatus(ridershift.Status(req.Status)).
		Save(r.Context())
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update shift status"})
		return
	}

	respondJSON(w, http.StatusOK, shift)
}

// StartShift marks a shift as active.
// POST /api/v1/{tenant}/shifts/{shiftId}/start
func (h *ShiftHandler) StartShift(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(chi.URLParam(r, "shiftId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shift ID"})
		return
	}

	shift, err := h.client.RiderShift.UpdateOneID(sid).
		SetStatus(ridershift.StatusActive).
		Save(r.Context())
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to start shift"})
		return
	}

	respondJSON(w, http.StatusOK, shift)
}

// EndShift marks a shift as completed.
// POST /api/v1/{tenant}/shifts/{shiftId}/end
func (h *ShiftHandler) EndShift(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(chi.URLParam(r, "shiftId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shift ID"})
		return
	}

	shift, err := h.client.RiderShift.UpdateOneID(sid).
		SetStatus(ridershift.StatusCompleted).
		Save(r.Context())
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to end shift"})
		return
	}

	respondJSON(w, http.StatusOK, shift)
}

// DeleteShift cancels/deletes a shift.
// DELETE /api/v1/{tenant}/shifts/{shiftId}
func (h *ShiftHandler) DeleteShift(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(chi.URLParam(r, "shiftId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shift ID"})
		return
	}

	if err := h.client.RiderShift.DeleteOneID(sid).Exec(r.Context()); err != nil {
		if ent.IsNotFound(err) {
			respondJSON(w, http.StatusNotFound, map[string]string{"error": "shift not found"})
			return
		}
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete shift"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}