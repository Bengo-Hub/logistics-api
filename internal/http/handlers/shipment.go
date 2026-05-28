package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/chainofcustody"
	"github.com/bengobox/logistics-service/internal/ent/shipment"
)

// ShipmentHandler handles distribution shipment operations.
type ShipmentHandler struct {
	client *ent.Client
	logger *zap.Logger
}

// NewShipmentHandler creates a new ShipmentHandler.
func NewShipmentHandler(client *ent.Client, logger *zap.Logger) *ShipmentHandler {
	return &ShipmentHandler{client: client, logger: logger.Named("shipments")}
}

func (h *ShipmentHandler) RegisterRoutes(r chi.Router) {
	r.Route("/shipments", func(s chi.Router) {
		s.Get("/", h.ListShipments)
		s.Post("/", h.CreateShipment)
		s.Get("/{shipmentId}", h.GetShipment)
		s.Patch("/{shipmentId}/status", h.UpdateShipmentStatus)
		s.Post("/{shipmentId}/dispatch", h.DispatchShipment)
		s.Get("/{shipmentId}/custody", h.ListCustody)
		s.Post("/{shipmentId}/custody", h.AddCustodyEvent)
	})
}

// ListShipments returns paginated shipments for a tenant.
// GET /api/v1/{tenant}/shipments
func (h *ShipmentHandler) ListShipments(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	q := h.client.Shipment.Query().
		Where(shipment.TenantID(tenantID))

	if s := r.URL.Query().Get("status"); s != "" {
		q = q.Where(shipment.StatusEQ(s))
	}

	shipments, err := q.Order(ent.Desc(shipment.FieldCreatedAt)).Limit(50).All(r.Context())
	if err != nil {
		h.logger.Error("list shipments", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list shipments"})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"data": shipments, "total": len(shipments)})
}

type createShipmentReq struct {
	ShipmentType       string    `json:"shipment_type"`
	SourceFacilityID   *string   `json:"source_facility_id,omitempty"`
	SourceFacilityName string    `json:"source_facility_name"`
	DestFacilityID     *string   `json:"dest_facility_id,omitempty"`
	DestFacilityName   string    `json:"dest_facility_name"`
	TempMinCelsius     *float64  `json:"temperature_min_celsius,omitempty"`
	TempMaxCelsius     *float64  `json:"temperature_max_celsius,omitempty"`
	SpecialHandling    []string  `json:"special_handling"`
	SealNumber         string    `json:"seal_number,omitempty"`
	PlannedDispatchAt  *string   `json:"planned_dispatch_at,omitempty"`
	ExternalReference  string    `json:"external_reference,omitempty"`
}

// CreateShipment creates a new distribution shipment batch.
// POST /api/v1/{tenant}/shipments
func (h *ShipmentHandler) CreateShipment(w http.ResponseWriter, r *http.Request) {
	tenantID := tenantIDFromClaims(r)
	if tenantID == uuid.Nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req createShipmentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	code := generateShipmentCode()

	create := h.client.Shipment.Create().
		SetTenantID(tenantID).
		SetShipmentCode(code).
		SetShipmentType(strOrDefault(req.ShipmentType, "warehouse_transfer")).
		SetSourceFacilityName(req.SourceFacilityName).
		SetDestFacilityName(req.DestFacilityName)

	if req.SourceFacilityID != nil {
		if id, err := uuid.Parse(*req.SourceFacilityID); err == nil {
			create = create.SetSourceFacilityID(id)
		}
	}
	if req.DestFacilityID != nil {
		if id, err := uuid.Parse(*req.DestFacilityID); err == nil {
			create = create.SetDestFacilityID(id)
		}
	}
	if req.TempMinCelsius != nil {
		create = create.SetTemperatureMinCelsius(*req.TempMinCelsius)
	}
	if req.TempMaxCelsius != nil {
		create = create.SetTemperatureMaxCelsius(*req.TempMaxCelsius)
	}
	if len(req.SpecialHandling) > 0 {
		create = create.SetSpecialHandling(req.SpecialHandling)
	}
	if req.SealNumber != "" {
		create = create.SetSealNumber(req.SealNumber)
	}
	if req.PlannedDispatchAt != nil {
		if t, err := time.Parse(time.RFC3339, *req.PlannedDispatchAt); err == nil {
			create = create.SetPlannedDispatchAt(t)
		}
	}
	if req.ExternalReference != "" {
		create = create.SetExternalReference(req.ExternalReference)
	}

	s, err := create.Save(r.Context())
	if err != nil {
		h.logger.Error("create shipment", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create shipment"})
		return
	}

	respondJSON(w, http.StatusCreated, s)
}

// GetShipment returns a single shipment with chain of custody.
// GET /api/v1/{tenant}/shipments/{shipmentId}
func (h *ShipmentHandler) GetShipment(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(chi.URLParam(r, "shipmentId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shipment ID"})
		return
	}

	s, err := h.client.Shipment.Query().
		Where(shipment.ID(sid)).
		WithChainOfCustody(func(q *ent.ChainOfCustodyQuery) {
			q.Order(ent.Asc(chainofcustody.FieldOccurredAt))
		}).
		Only(r.Context())
	if err != nil {
		if ent.IsNotFound(err) {
			respondJSON(w, http.StatusNotFound, map[string]string{"error": "shipment not found"})
			return
		}
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get shipment"})
		return
	}

	respondJSON(w, http.StatusOK, s)
}

// UpdateShipmentStatus updates a shipment status.
// PATCH /api/v1/{tenant}/shipments/{shipmentId}/status
func (h *ShipmentHandler) UpdateShipmentStatus(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(chi.URLParam(r, "shipmentId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shipment ID"})
		return
	}

	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Status == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "status is required"})
		return
	}

	update := h.client.Shipment.UpdateOneID(sid).SetStatus(req.Status)
	if req.Status == "completed" {
		update = update.SetCompletedAt(time.Now())
	}

	s, err := update.Save(r.Context())
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update status"})
		return
	}

	respondJSON(w, http.StatusOK, s)
}

// DispatchShipment marks a shipment as in_transit and records dispatch time.
// POST /api/v1/{tenant}/shipments/{shipmentId}/dispatch
func (h *ShipmentHandler) DispatchShipment(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(chi.URLParam(r, "shipmentId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shipment ID"})
		return
	}

	s, err := h.client.Shipment.UpdateOneID(sid).
		SetStatus("in_transit").
		SetDispatchedAt(time.Now()).
		Save(r.Context())
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to dispatch shipment"})
		return
	}

	respondJSON(w, http.StatusOK, s)
}

// ListCustody returns the chain of custody for a shipment.
// GET /api/v1/{tenant}/shipments/{shipmentId}/custody
func (h *ShipmentHandler) ListCustody(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(chi.URLParam(r, "shipmentId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shipment ID"})
		return
	}

	records, err := h.client.ChainOfCustody.Query().
		Where(chainofcustody.ShipmentID(sid)).
		Order(ent.Asc(chainofcustody.FieldOccurredAt)).
		All(r.Context())
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list custody records"})
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"data": records, "total": len(records)})
}

type addCustodyReq struct {
	ActorName          string   `json:"actor_name"`
	EventType          string   `json:"event_type"`
	LocationName       string   `json:"location_name,omitempty"`
	Latitude           *float64 `json:"latitude,omitempty"`
	Longitude          *float64 `json:"longitude,omitempty"`
	Notes              string   `json:"notes,omitempty"`
	PhotoURL           string   `json:"photo_url,omitempty"`
	SignatureURL       string   `json:"signature_url,omitempty"`
	TemperatureReading *float64 `json:"temperature_reading,omitempty"`
	ReceivedQuantity   *int     `json:"received_quantity,omitempty"`
	ReceivingStaffName string   `json:"receiving_staff_name,omitempty"`
	TaskID             *string  `json:"task_id,omitempty"`
}

// AddCustodyEvent adds a chain of custody event to a shipment.
// POST /api/v1/{tenant}/shipments/{shipmentId}/custody
func (h *ShipmentHandler) AddCustodyEvent(w http.ResponseWriter, r *http.Request) {
	sid, err := uuid.Parse(chi.URLParam(r, "shipmentId"))
	if err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid shipment ID"})
		return
	}

	claims, ok := authclient.ClaimsFromContext(r.Context())
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	actorID, _ := uuid.Parse(claims.Subject)

	var req addCustodyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.EventType == "" {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "event_type is required"})
		return
	}

	create := h.client.ChainOfCustody.Create().
		SetShipmentID(sid).
		SetActorID(actorID).
		SetActorName(req.ActorName).
		SetEventType(req.EventType)

	if req.LocationName != "" {
		create = create.SetLocationName(req.LocationName)
	}
	if req.Latitude != nil {
		create = create.SetLatitude(*req.Latitude)
	}
	if req.Longitude != nil {
		create = create.SetLongitude(*req.Longitude)
	}
	if req.Notes != "" {
		create = create.SetNotes(req.Notes)
	}
	if req.PhotoURL != "" {
		create = create.SetPhotoURL(req.PhotoURL)
	}
	if req.SignatureURL != "" {
		create = create.SetSignatureURL(req.SignatureURL)
	}
	if req.TemperatureReading != nil {
		create = create.SetTemperatureReading(*req.TemperatureReading)
	}
	if req.ReceivedQuantity != nil {
		create = create.SetReceivedQuantity(*req.ReceivedQuantity)
	}
	if req.ReceivingStaffName != "" {
		create = create.SetReceivingStaffName(req.ReceivingStaffName)
	}
	if req.TaskID != nil {
		if tid, err := uuid.Parse(*req.TaskID); err == nil {
			create = create.SetTaskID(tid)
		}
	}

	record, err := create.Save(r.Context())
	if err != nil {
		h.logger.Error("add custody event", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to add custody event"})
		return
	}

	respondJSON(w, http.StatusCreated, record)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func generateShipmentCode() string {
	return fmt.Sprintf("SHIP-%d-%s", time.Now().Year(), uuid.New().String()[:6])
}

func strOrDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
