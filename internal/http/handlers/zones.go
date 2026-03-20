package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/zones"
)

// ZonesHandler handles delivery zone CRUD endpoints.
type ZonesHandler struct {
	svc *zones.Service
	log *zap.Logger
}

// NewZonesHandler creates a new zones handler.
func NewZonesHandler(svc *zones.Service, log *zap.Logger) *ZonesHandler {
	return &ZonesHandler{
		svc: svc,
		log: log.Named("handlers.zones"),
	}
}

// ListZones handles GET /{tenant}/zones
func (h *ZonesHandler) ListZones(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	list, err := h.svc.ListZones(r.Context(), tenantID)
	if err != nil {
		h.log.Error("list zones", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	respondJSON(w, http.StatusOK, list)
}

// CreateZone handles POST /{tenant}/zones
func (h *ZonesHandler) CreateZone(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var req zones.CreateZoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	zone, err := h.svc.CreateZone(r.Context(), tenantID, req)
	if err != nil {
		h.log.Warn("create zone", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusCreated, zone)
}

// GetZone handles GET /{tenant}/zones/{zoneId}
func (h *ZonesHandler) GetZone(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	zoneID, err := uuid.Parse(chi.URLParam(r, "zoneId"))
	if err != nil {
		http.Error(w, "invalid zone id", http.StatusBadRequest)
		return
	}

	zone, err := h.svc.GetZone(r.Context(), tenantID, zoneID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	respondJSON(w, http.StatusOK, zone)
}

// UpdateZone handles PATCH /{tenant}/zones/{zoneId}
func (h *ZonesHandler) UpdateZone(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	zoneID, err := uuid.Parse(chi.URLParam(r, "zoneId"))
	if err != nil {
		http.Error(w, "invalid zone id", http.StatusBadRequest)
		return
	}

	var req zones.UpdateZoneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	zone, err := h.svc.UpdateZone(r.Context(), tenantID, zoneID, req)
	if err != nil {
		h.log.Warn("update zone", zap.Error(err))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	respondJSON(w, http.StatusOK, zone)
}

// DeleteZone handles DELETE /{tenant}/zones/{zoneId}
func (h *ZonesHandler) DeleteZone(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	zoneID, err := uuid.Parse(chi.URLParam(r, "zoneId"))
	if err != nil {
		http.Error(w, "invalid zone id", http.StatusBadRequest)
		return
	}

	if err := h.svc.DeleteZone(r.Context(), tenantID, zoneID); err != nil {
		h.log.Warn("delete zone", zap.Error(err))
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
