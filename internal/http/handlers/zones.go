package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/zones"
	"github.com/bengobox/logistics-service/internal/modules/zones/geo"
)

// ZonesHandler serves service areas, the delivery quote policy, delivery quotes and the
// geocoding proxy. It is the platform's single delivery-area API.
type ZonesHandler struct {
	svc         *zones.Service
	geocoder    *zones.Geocoder
	resolveSlug func(ctx context.Context, slug string) (uuid.UUID, error)
	log         *zap.Logger
}

// NewZonesHandler creates a new zones handler.
func NewZonesHandler(svc *zones.Service, log *zap.Logger) *ZonesHandler {
	return &ZonesHandler{svc: svc, log: log.Named("handlers.zones")}
}

// SetGeocoder enables the place search and reverse lookup endpoints.
func (h *ZonesHandler) SetGeocoder(g *zones.Geocoder) { h.geocoder = g }

// SetSlugResolver lets S2S callers address a tenant by slug as well as by UUID.
func (h *ZonesHandler) SetSlugResolver(f func(ctx context.Context, slug string) (uuid.UUID, error)) {
	h.resolveSlug = f
}

func (h *ZonesHandler) zoneError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, zones.ErrNotFound):
		respondJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, zones.ErrDuplicate):
		respondJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	default:
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
}

// ListZones godoc
// @Summary List delivery zones
// @Description All service areas of the tenant (any status) with their delivery settings.
// @Tags Zones
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Success 200 {array} zones.ZoneView
// @Router /{tenant}/zones [get]
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

// CreateZone godoc
// @Summary Create a delivery zone
// @Description Circle (settings.center + settings.radius_m) or polygon (boundary as [[lng,lat],...]).
// @Tags Zones
// @Accept json
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param body body zones.ZoneInput true "Zone"
// @Success 201 {object} zones.ZoneView
// @Failure 400 {object} map[string]string
// @Failure 409 {object} map[string]string
// @Router /{tenant}/zones [post]
func (h *ZonesHandler) CreateZone(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	var in zones.ZoneInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	z, err := h.svc.CreateZone(r.Context(), tenantID, in)
	if err != nil {
		h.zoneError(w, err)
		return
	}
	respondJSON(w, http.StatusCreated, z)
}

// GetZone godoc
// @Summary Get a delivery zone
// @Tags Zones
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param zoneId path string true "Zone ID"
// @Success 200 {object} zones.ZoneView
// @Router /{tenant}/zones/{zoneId} [get]
func (h *ZonesHandler) GetZone(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	zoneID, err := uuid.Parse(chi.URLParam(r, "zoneId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid zone id"})
		return
	}
	z, err := h.svc.GetZone(r.Context(), tenantID, zoneID)
	if err != nil {
		h.zoneError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, z)
}

// UpdateZone godoc
// @Summary Update a delivery zone
// @Tags Zones
// @Accept json
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param zoneId path string true "Zone ID"
// @Param body body zones.ZonePatch true "Fields to change"
// @Success 200 {object} zones.ZoneView
// @Router /{tenant}/zones/{zoneId} [patch]
func (h *ZonesHandler) UpdateZone(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	zoneID, err := uuid.Parse(chi.URLParam(r, "zoneId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid zone id"})
		return
	}
	var p zones.ZonePatch
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	z, err := h.svc.UpdateZone(r.Context(), tenantID, zoneID, p)
	if err != nil {
		h.zoneError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, z)
}

// DeleteZone godoc
// @Summary Delete a delivery zone
// @Tags Zones
// @Param tenant path string true "Tenant slug"
// @Param zoneId path string true "Zone ID"
// @Success 204
// @Router /{tenant}/zones/{zoneId} [delete]
func (h *ZonesHandler) DeleteZone(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	zoneID, err := uuid.Parse(chi.URLParam(r, "zoneId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid zone id"})
		return
	}
	if err := h.svc.DeleteZone(r.Context(), tenantID, zoneID); err != nil {
		h.zoneError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Coverage godoc
// @Summary Public delivery coverage
// @Description Active zones, outlets and map bounds for customer maps. Public.
// @Tags Zones
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param outlet_id query string false "Limit to zones serving this outlet"
// @Success 200 {object} zones.Coverage
// @Router /{tenant}/zones/coverage [get]
func (h *ZonesHandler) Coverage(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	outletID, _ := uuid.Parse(r.URL.Query().Get("outlet_id"))
	c, err := h.svc.Coverage(r.Context(), tenantID, outletID)
	if err != nil {
		h.log.Error("coverage", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	respondJSON(w, http.StatusOK, c)
}

// QuoteRequest is the body of the S2S quote endpoint.
type QuoteRequest struct {
	Lat        float64   `json:"lat"`
	Lng        float64   `json:"lng"`
	OutletID   uuid.UUID `json:"outlet_id"`
	OrderTotal float64   `json:"order_total"`
}

// Quote godoc
// @Summary Delivery quote for a location
// @Description Public. Returns serviceability, fee, matched area and ETA. The fee is authoritative.
// @Tags Zones
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param lat query number true "Latitude"
// @Param lng query number true "Longitude"
// @Param outlet_id query string false "Dispatching outlet (nearest when omitted)"
// @Param order_total query number false "Basket total, flags min-order shortfalls"
// @Success 200 {object} zones.Quote
// @Router /{tenant}/zones/quote [get]
func (h *ZonesHandler) Quote(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "tenant not found", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
	lng, err2 := strconv.ParseFloat(q.Get("lng"), 64)
	if err1 != nil || err2 != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "lat and lng are required"})
		return
	}
	outletID, _ := uuid.Parse(q.Get("outlet_id"))
	total, _ := strconv.ParseFloat(q.Get("order_total"), 64)
	h.writeQuote(w, r, tenantID, zones.QuoteInput{Point: geo.Point{Lat: lat, Lng: lng}, OutletID: outletID, OrderTotal: total})
}

// S2SQuote godoc
// @Summary Delivery quote (service-to-service)
// @Description Authoritative quote for checkout in ordering, pos and other services. X-API-Key auth.
// @Tags S2S
// @Accept json
// @Produce json
// @Param tenant path string true "Tenant UUID or slug"
// @Param body body QuoteRequest true "Location"
// @Success 200 {object} zones.Quote
// @Router /s2s/zones/{tenant}/quote [post]
func (h *ZonesHandler) S2SQuote(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.s2sTenant(r)
	if !ok {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "tenant not found"})
		return
	}
	var req QuoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	h.writeQuote(w, r, tenantID, zones.QuoteInput{Point: geo.Point{Lat: req.Lat, Lng: req.Lng}, OutletID: req.OutletID, OrderTotal: req.OrderTotal})
}

// S2SCoverage godoc
// @Summary Delivery coverage (service-to-service)
// @Tags S2S
// @Produce json
// @Param tenant path string true "Tenant UUID or slug"
// @Param outlet_id query string false "Outlet"
// @Success 200 {object} zones.Coverage
// @Router /s2s/zones/{tenant}/coverage [get]
func (h *ZonesHandler) S2SCoverage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.s2sTenant(r)
	if !ok {
		respondJSON(w, http.StatusNotFound, map[string]string{"error": "tenant not found"})
		return
	}
	outletID, _ := uuid.Parse(r.URL.Query().Get("outlet_id"))
	c, err := h.svc.Coverage(r.Context(), tenantID, outletID)
	if err != nil {
		h.log.Error("s2s coverage", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, c)
}

func (h *ZonesHandler) s2sTenant(r *http.Request) (uuid.UUID, bool) {
	raw := chi.URLParam(r, "tenant")
	if id, err := uuid.Parse(raw); err == nil {
		return id, true
	}
	if h.resolveSlug != nil && raw != "" {
		if id, err := h.resolveSlug(r.Context(), raw); err == nil {
			return id, true
		}
	}
	return uuid.Nil, false
}

func (h *ZonesHandler) writeQuote(w http.ResponseWriter, r *http.Request, tenantID uuid.UUID, in zones.QuoteInput) {
	quote, err := h.svc.Quote(r.Context(), tenantID, in)
	if err != nil {
		h.log.Error("quote", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, quote)
}

// GetPolicy godoc
// @Summary Delivery quote policy
// @Description Per-km fallback, geofence buffer, rounding and distance settings.
// @Tags Zones
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Success 200 {object} zones.PolicyView
// @Router /{tenant}/delivery-policy [get]
func (h *ZonesHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	pv, err := h.svc.GetPolicy(r.Context(), tenantID)
	if err != nil {
		h.log.Error("get policy", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, pv)
}

// SavePolicy godoc
// @Summary Save the delivery quote policy
// @Tags Zones
// @Accept json
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param body body zones.Policy true "Policy"
// @Success 200 {object} zones.PolicyView
// @Router /{tenant}/delivery-policy [put]
func (h *ZonesHandler) SavePolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	p := zones.DefaultPolicy()
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	pv, err := h.svc.SavePolicy(r.Context(), tenantID, p)
	if err != nil {
		h.zoneError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, pv)
}

// ResetPolicy godoc
// @Summary Drop the tenant's own delivery policy and follow the platform default
// @Tags Zones
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Success 200 {object} zones.PolicyView
// @Router /{tenant}/delivery-policy [delete]
func (h *ZonesHandler) ResetPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	pv, err := h.svc.ResetPolicy(r.Context(), tenantID)
	if err != nil {
		h.log.Error("reset policy", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, pv)
}

// GetPlatformPolicy godoc
// @Summary Platform default delivery policy (platform owners)
// @Tags Zones
// @Produce json
// @Success 200 {object} zones.PolicyView
// @Router /admin/delivery-policy [get]
func (h *ZonesHandler) GetPlatformPolicy(w http.ResponseWriter, r *http.Request) {
	pv, err := h.svc.GetPlatformPolicy(r.Context())
	if err != nil {
		h.log.Error("get platform policy", zap.Error(err))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	respondJSON(w, http.StatusOK, pv)
}

// SavePlatformPolicy godoc
// @Summary Save the platform default delivery policy (used by tenants without their own)
// @Tags Zones
// @Accept json
// @Produce json
// @Param body body zones.Policy true "Policy"
// @Success 200 {object} zones.PolicyView
// @Router /admin/delivery-policy [put]
func (h *ZonesHandler) SavePlatformPolicy(w http.ResponseWriter, r *http.Request) {
	p := zones.DefaultPolicy()
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	pv, err := h.svc.SavePlatformPolicy(r.Context(), p)
	if err != nil {
		h.zoneError(w, err)
		return
	}
	respondJSON(w, http.StatusOK, pv)
}

// GeocodeSearch godoc
// @Summary Search places
// @Description Tenant delivery areas first, then geocoder results biased to the coverage. Public.
// @Tags Routing
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param q query string true "Text to search"
// @Param limit query int false "Max results (10)"
// @Success 200 {array} zones.Place
// @Router /{tenant}/routing/geocode/search [get]
func (h *ZonesHandler) GeocodeSearch(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil || h.geocoder == nil {
		http.Error(w, "not available", http.StatusNotFound)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	places, err := h.geocoder.Search(r.Context(), tenantID, r.URL.Query().Get("q"), limit)
	if err != nil {
		h.log.Warn("geocode search", zap.Error(err))
		http.Error(w, "geocoder unavailable", http.StatusBadGateway)
		return
	}
	respondJSON(w, http.StatusOK, places)
}

// GeocodeReverse godoc
// @Summary Name the place at a location
// @Description Place name plus the tenant delivery area it falls in or is nearest to. Public.
// @Tags Routing
// @Produce json
// @Param tenant path string true "Tenant slug"
// @Param lat query number true "Latitude"
// @Param lng query number true "Longitude"
// @Success 200 {object} zones.Place
// @Router /{tenant}/routing/geocode/reverse [get]
func (h *ZonesHandler) GeocodeReverse(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil || h.geocoder == nil {
		http.Error(w, "not available", http.StatusNotFound)
		return
	}
	lat, err1 := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lng, err2 := strconv.ParseFloat(r.URL.Query().Get("lng"), 64)
	if err1 != nil || err2 != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "lat and lng are required"})
		return
	}
	place, err := h.geocoder.Reverse(r.Context(), tenantID, geo.Point{Lat: lat, Lng: lng})
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	respondJSON(w, http.StatusOK, place)
}
