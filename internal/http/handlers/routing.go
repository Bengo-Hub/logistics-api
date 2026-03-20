package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/routing"
)

// RoutingHandler handles routing API endpoints.
type RoutingHandler struct {
	svc *routing.Service
	log *zap.Logger
}

// NewRoutingHandler creates a new routing handler.
func NewRoutingHandler(svc *routing.Service, log *zap.Logger) *RoutingHandler {
	return &RoutingHandler{
		svc: svc,
		log: log.Named("handlers.routing"),
	}
}

// GetRoute handles GET /{tenant}/routing/route?from_lat=&from_lng=&to_lat=&to_lng=
func (h *RoutingHandler) GetRoute(w http.ResponseWriter, r *http.Request) {
	origin, destination, err := parseRouteParams(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	route, err := h.svc.Route(r.Context(), origin, destination)
	if err != nil {
		h.log.Error("route calculation failed", zap.Error(err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "routing engine unavailable"})
		return
	}

	writeJSON(w, http.StatusOK, route)
}

// GetETA handles GET /{tenant}/routing/eta?from_lat=&from_lng=&to_lat=&to_lng=
func (h *RoutingHandler) GetETA(w http.ResponseWriter, r *http.Request) {
	origin, destination, err := parseRouteParams(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	etaMinutes, err := h.svc.ETAMinutes(r.Context(), origin, destination)
	if err != nil {
		h.log.Error("ETA calculation failed", zap.Error(err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "routing engine unavailable"})
		return
	}

	distKm, _ := h.svc.DistanceKm(r.Context(), origin, destination)

	writeJSON(w, http.StatusOK, map[string]any{
		"eta_minutes": etaMinutes,
		"distance_km": distKm,
	})
}

// GetMatrix handles POST /{tenant}/routing/matrix
func (h *RoutingHandler) GetMatrix(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Origins      []routing.LatLng `json:"origins"`
		Destinations []routing.LatLng `json:"destinations"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	if len(req.Origins) == 0 || len(req.Destinations) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "origins and destinations required"})
		return
	}

	matrix, err := h.svc.Matrix(r.Context(), req.Origins, req.Destinations)
	if err != nil {
		h.log.Error("matrix calculation failed", zap.Error(err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "routing engine unavailable"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"matrix": matrix})
}

// GetIsochrone handles GET /{tenant}/routing/isochrone?lat=&lng=&time_minutes=
func (h *RoutingHandler) GetIsochrone(w http.ResponseWriter, r *http.Request) {
	lat, err := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid lat"})
		return
	}
	lng, err := strconv.ParseFloat(r.URL.Query().Get("lng"), 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid lng"})
		return
	}
	timeMin, err := strconv.ParseFloat(r.URL.Query().Get("time_minutes"), 64)
	if err != nil || timeMin <= 0 {
		timeMin = 15 // default 15 minutes
	}

	iso, err := h.svc.Isochrone(r.Context(), routing.LatLng{Lat: lat, Lng: lng}, timeMin)
	if err != nil {
		h.log.Error("isochrone calculation failed", zap.Error(err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "routing engine unavailable"})
		return
	}

	writeJSON(w, http.StatusOK, iso)
}

// HealthCheck handles GET /routing/health
func (h *RoutingHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	result := h.svc.Health(r.Context())
	status := http.StatusOK
	resp := map[string]string{}

	for name, err := range result {
		if err != nil {
			resp[name] = err.Error()
			status = http.StatusServiceUnavailable
		} else {
			resp[name] = "healthy"
		}
	}

	writeJSON(w, status, resp)
}

func parseRouteParams(r *http.Request) (routing.LatLng, routing.LatLng, error) {
	q := r.URL.Query()
	fromLat, err := strconv.ParseFloat(q.Get("from_lat"), 64)
	if err != nil {
		return routing.LatLng{}, routing.LatLng{}, err
	}
	fromLng, err := strconv.ParseFloat(q.Get("from_lng"), 64)
	if err != nil {
		return routing.LatLng{}, routing.LatLng{}, err
	}
	toLat, err := strconv.ParseFloat(q.Get("to_lat"), 64)
	if err != nil {
		return routing.LatLng{}, routing.LatLng{}, err
	}
	toLng, err := strconv.ParseFloat(q.Get("to_lng"), 64)
	if err != nil {
		return routing.LatLng{}, routing.LatLng{}, err
	}

	return routing.LatLng{Lat: fromLat, Lng: fromLng}, routing.LatLng{Lat: toLat, Lng: toLng}, nil
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
