package tasks

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleet"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/serviceconfig"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	entuser "github.com/bengobox/logistics-service/internal/ent/user"
	"github.com/bengobox/logistics-service/internal/platform/events"
)

// Valid task statuses (state machine).
//
// "en_route"/"delivered" are the original coarse states -- still valid (SubmitPoD sets
// "delivered" directly regardless of which branch got the task there, and some older
// callers may still use "en_route"). "en_route_pickup" through "arrived_dropoff" are the
// granular per-leg states the rider-app's UI has always been built against (distinct
// "driving to the outlet" vs "driving to the customer" phases, matching Bolt/Uber-style
// tracking) but which this table never actually accepted -- confirmed live: a real rider
// PATCHing to "en_route_pickup" from "accepted" got a 400 and could never advance past
// acceptance. Both vocabularies are accepted so neither existing nor new callers break.
var validTransitions = map[string][]string{
	"pending":          {"assigned", "cancelled"},
	"assigned":         {"accepted", "cancelled"},
	"accepted":         {"en_route", "en_route_pickup", "cancelled"},
	"en_route":         {"delivered", "failed"},
	"en_route_pickup":  {"arrived_pickup", "cancelled", "failed"},
	"arrived_pickup":   {"picked_up", "cancelled", "failed"},
	"picked_up":        {"en_route_dropoff", "cancelled", "failed"},
	"en_route_dropoff": {"arrived_dropoff", "cancelled", "failed"},
	"arrived_dropoff":  {"delivered", "cancelled", "failed"},
	"delivered":        {},
	"completed":        {},
	"failed":           {},
	"cancelled":        {},
}

// CreateTaskRequest is the DTO for creating a task.
type CreateTaskRequest struct {
	ExternalReference string         `json:"external_reference"`
	SourceService     string         `json:"source_service"`
	TaskType          string         `json:"task_type"`
	Priority          int            `json:"priority"`
	SLADueAt          *time.Time     `json:"sla_due_at,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`

	// Delivery-specific fields (optional). When provided, pickup and dropoff
	// TaskStep records are created automatically.
	PickupAddress  string  `json:"pickup_address,omitempty"`
	PickupLat      float64 `json:"pickup_lat,omitempty"`
	PickupLng      float64 `json:"pickup_lng,omitempty"`
	PickupContact  string  `json:"pickup_contact,omitempty"`
	DropoffAddress string  `json:"dropoff_address,omitempty"`
	DropoffLat     float64 `json:"dropoff_lat,omitempty"`
	DropoffLng     float64 `json:"dropoff_lng,omitempty"`
	DropoffContact string  `json:"dropoff_contact,omitempty"`
	CustomerName   string  `json:"customer_name,omitempty"`
	CustomerPhone  string  `json:"customer_phone,omitempty"`
	Instructions   string  `json:"instructions,omitempty"`
}

// CreateTaskFromOrderRequest carries delivery context from the ordering event.
type CreateTaskFromOrderRequest struct {
	OrderID         string  `json:"order_id"`
	OrderNumber     string  `json:"order_number"`
	CustomerName    string  `json:"customer_name"`
	CustomerPhone   string  `json:"customer_phone"`
	Instructions    string  `json:"instructions"`
	CashOnDelivery  float64 `json:"cash_on_delivery"`
	FulfillmentType string  `json:"fulfillment_type"`
	PickupName      string  `json:"pickup_name"`
	PickupLat       float64 `json:"pickup_lat"`
	PickupLng       float64 `json:"pickup_lng"`
	DropoffName     string  `json:"dropoff_name"`
	DropoffLat      float64 `json:"dropoff_lat"`
	DropoffLng      float64 `json:"dropoff_lng"`
	// Outlet (pickup point) details for the rider: which branch, where the counter is, who to call.
	OutletID      string `json:"outlet_id"`
	PickupAddress string `json:"pickup_address"`
	PickupPhone   string `json:"pickup_phone"`
	// PODCode is the customer's proof-of-delivery code. It is kept in task metadata to verify the
	// code the rider enters and is never returned to API clients (see handlers.publicTaskMetadata).
	PODCode       string                   `json:"pod_code"`
	DeliveryFee   float64                  `json:"delivery_fee"`
	PaymentMethod string                   `json:"payment_method"`
	Items         []map[string]interface{} `json:"items"`
}

// AssignTaskRequest is the DTO for assigning a task to a fleet member.
type AssignTaskRequest struct {
	FleetMemberID uuid.UUID `json:"fleet_member_id"`
}

// SubmitPoDRequest is the DTO for submitting proof of delivery.
type SubmitPoDRequest struct {
	FleetMemberID    uuid.UUID `json:"fleet_member_id"`
	SignatureURL     string    `json:"signature_url,omitempty"`
	PhotoURL         string    `json:"photo_url,omitempty"`
	OTPCode          string    `json:"otp_code,omitempty"`
	ConfirmationCode string    `json:"confirmation_code,omitempty"`
	AmountCollected  float64   `json:"amount_collected,omitempty"`
	// CollectionMethod is how the customer paid at the door: "cash" or "mpesa" (to the business
	// Till/Paybill). CollectionReference is the M-Pesa code for an mpesa collection.
	CollectionMethod    string         `json:"collection_method,omitempty"`
	CollectionReference string         `json:"collection_reference,omitempty"`
	RecipientName       string         `json:"recipient_name,omitempty"`
	Notes               string         `json:"notes,omitempty"`
	Latitude            *float64       `json:"latitude,omitempty"`
	Longitude           *float64       `json:"longitude,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
}

// podAllowedFrom lists the statuses a task can be delivered from: the rider must have collected
// the order. A pending/assigned/accepted task (still at the outlet) cannot be proof-of-delivered.
var podAllowedFrom = map[string]bool{
	"picked_up": true, "en_route_dropoff": true, "arrived_dropoff": true, "en_route": true,
}

// describeItems renders an order's lines as "2x Burger, 1x Chips" plus the unit count, so the
// rider can check the bag at the counter.
func describeItems(items []map[string]interface{}) (string, int) {
	parts := make([]string, 0, len(items))
	count := 0
	for _, it := range items {
		name, _ := it["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		qty := int(metadataNumber(it["quantity"]))
		if qty <= 0 {
			qty = 1
		}
		count += qty
		parts = append(parts, fmt.Sprintf("%dx %s", qty, name))
	}
	return strings.Join(parts, ", "), count
}

// metadataString reads a string metadata value ("" when absent).
func metadataString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return v
}

// taskCODAmount returns the cash the rider must collect, from the column or (for tasks created
// from an order, which carry it in metadata) the metadata.
func taskCODAmount(t *ent.Task) float64 {
	if t.CashOnDelivery > 0 {
		return t.CashOnDelivery
	}
	if t.Metadata != nil {
		return metadataNumber(t.Metadata["cash_on_delivery"])
	}
	return 0
}

// AutoAssignEnabled reports whether the tenant wants new delivery tasks auto-assigned to the
// nearest rider (setting logistics.auto_assign_enabled: the tenant's own value, else the platform
// default, else on). A business with its own dispatcher turns it off and assigns from the board.
func (s *Service) AutoAssignEnabled(ctx context.Context, tenantID uuid.UUID) bool {
	return s.boolSetting(ctx, tenantID, "logistics.auto_assign_enabled", true)
}

// boolSetting reads a boolean service setting: the tenant's own value, else the platform
// default, else def.
func (s *Service) boolSetting(ctx context.Context, tenantID uuid.UUID, key string, def bool) bool {
	cfg, err := s.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(key), serviceconfig.TenantID(tenantID)).
		First(ctx)
	if err != nil {
		cfg, err = s.client.ServiceConfig.Query().
			Where(serviceconfig.ConfigKey(key), serviceconfig.TenantIDIsNil()).
			First(ctx)
	}
	if err != nil {
		return def
	}
	v, perr := strconv.ParseBool(strings.TrimSpace(cfg.ConfigValue))
	if perr != nil {
		return def
	}
	return v
}

// ActiveAssignee returns the fleet member currently assigned to the task, if any.
func (s *Service) ActiveAssignee(ctx context.Context, taskID uuid.UUID) (uuid.UUID, bool) {
	a, err := s.client.TaskAssignment.Query().
		Where(taskassignment.TaskID(taskID), taskassignment.StatusIn("assigned", "accepted")).
		First(ctx)
	if err != nil {
		return uuid.Nil, false
	}
	return a.FleetMemberID, true
}

// ListTasksFilter holds optional filters for listing tasks.
type ListTasksFilter struct {
	Status string
	// Statuses, when non-empty, OR-matches any of these statuses (task.StatusIn) instead of
	// Status's exact match -- used for a coarse UI tab like "En Route" that maps to more than
	// one of the granular FSM states (en_route_pickup, en_route_dropoff).
	Statuses []string
	MemberID uuid.UUID
	OutletID *uuid.UUID
	// Search matches tracking_code or external_reference (case-insensitive substring).
	Search   string
	DateFrom *time.Time
	DateTo   *time.Time
	Limit    int
	Offset   int
}

// EarningsRecorder is the interface for recording delivery earnings.
type EarningsRecorder interface {
	RecordEarning(ctx context.Context, tenantID, taskID, memberID uuid.UUID, distanceKm float64) error
	// RecordEarningWithAmount records an earning using an explicit, known amount
	// (e.g. the order's actual delivery fee) rather than recomputing from distance.
	RecordEarningWithAmount(ctx context.Context, tenantID, taskID, memberID uuid.UUID, amount float64) error
}

// metadataNumber coerces a JSON-decoded metadata value into a float64.
// Task metadata round-trips through JSON, so numbers may arrive as float64,
// json.Number, or string depending on the source.
func metadataNumber(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}

// ETATrigger is the interface for triggering an ETA recalculation on a task.
type ETATrigger interface {
	ComputeAndPublishETA(ctx context.Context, tenantID, taskID uuid.UUID)
}

// StatusBroadcaster is the interface for pushing real-time task status events to SSE clients.
type StatusBroadcaster interface {
	Publish(tenantID, taskID uuid.UUID, event string, data any)
}

// Service handles task business logic.
type Service struct {
	client         *ent.Client
	log            *zap.Logger
	publisher      *events.Publisher
	earningsSvc    EarningsRecorder
	etaTrigger     ETATrigger
	sseBroadcaster StatusBroadcaster
}

// NewService creates a new task service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("tasks.service"),
	}
}

// Client returns the Ent client for direct queries (used by consumers for step creation).
func (s *Service) Client() *ent.Client {
	return s.client
}

// SetEarningsService sets the earnings service for recording delivery earnings.
func (s *Service) SetEarningsService(svc EarningsRecorder) {
	s.earningsSvc = svc
}

// SetPublisher sets the event publisher for task lifecycle events.
func (s *Service) SetPublisher(p *events.Publisher) {
	s.publisher = p
}

// SetETATrigger sets the ETA trigger for on-demand ETA recalculation on status changes.
func (s *Service) SetETATrigger(t ETATrigger) {
	s.etaTrigger = t
}

// SetSSEBroadcaster sets the SSE broadcaster for real-time task status streaming.
func (s *Service) SetSSEBroadcaster(b StatusBroadcaster) {
	s.sseBroadcaster = b
}

// CreateTask creates a new delivery task, optionally with pickup and dropoff steps.
func (s *Service) CreateTask(ctx context.Context, tenantID uuid.UUID, req CreateTaskRequest) (*ent.Task, error) {
	taskType := req.TaskType
	if taskType == "" {
		taskType = "delivery"
	}

	trackingCode := generateTrackingCode()

	builder := s.client.Task.Create().
		SetTenantID(tenantID).
		SetTrackingCode(trackingCode).
		SetTaskType(taskType).
		SetStatus("pending").
		SetPriority(req.Priority)

	if req.ExternalReference != "" {
		builder.SetExternalReference(req.ExternalReference)
	}
	if req.SourceService != "" {
		builder.SetSourceService(req.SourceService)
	}
	if req.SLADueAt != nil {
		builder.SetSLADueAt(*req.SLADueAt)
	}
	if req.Instructions != "" {
		meta := req.Metadata
		if meta == nil {
			meta = map[string]any{}
		}
		meta["instructions"] = req.Instructions
		builder.SetMetadata(meta)
	} else if req.Metadata != nil {
		builder.SetMetadata(req.Metadata)
	}

	// The dedicated Task.cash_on_delivery ent column (used by completion validation/cash-collection
	// logic below, e.g. the CashOnDelivery check in ConfirmDelivery) was never being set here — only
	// the generic metadata blob carried it, so COD tasks always validated against a zero column.
	// Every caller (including CreateTaskFromOrder) already threads the amount through Metadata, so
	// read it back out here once, at the single Task-creation choke point, instead of requiring every
	// caller to also call a setter.
	if req.Metadata != nil {
		if raw, ok := req.Metadata["cash_on_delivery"]; ok {
			if codAmount := metadataNumber(raw); codAmount > 0 {
				builder.SetCashOnDelivery(codAmount)
			}
		}
	}

	t, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: create: %w", err)
	}

	// Create pickup step if location is provided.
	if req.PickupAddress != "" || req.PickupLat != 0 {
		_, err = s.client.TaskStep.Create().
			SetTaskID(t.ID).
			SetStepType("pickup").
			SetSequence(1).
			SetLocationName(req.PickupAddress).
			SetAddressJSON(map[string]any{
				"address":   req.PickupAddress,
				"latitude":  req.PickupLat,
				"longitude": req.PickupLng,
			}).
			SetContactName(req.PickupContact).
			SetContactPhone("").
			SetRequiresSignature(false).
			SetRequiresPhoto(false).
			SetMetadata(map[string]any{}).
			Save(ctx)
		if err != nil {
			s.log.Warn("task step (pickup) create failed", zap.Error(err))
		}
	}

	// Create dropoff step if location is provided.
	if req.DropoffAddress != "" || req.DropoffLat != 0 {
		customerName := req.CustomerName
		if customerName == "" {
			customerName = req.DropoffContact
		}
		_, err = s.client.TaskStep.Create().
			SetTaskID(t.ID).
			SetStepType("dropoff").
			SetSequence(2).
			SetLocationName(req.DropoffAddress).
			SetAddressJSON(map[string]any{
				"address":   req.DropoffAddress,
				"latitude":  req.DropoffLat,
				"longitude": req.DropoffLng,
			}).
			SetContactName(customerName).
			SetContactPhone(req.CustomerPhone).
			SetRequiresSignature(true).
			SetRequiresPhoto(true).
			SetMetadata(map[string]any{}).
			Save(ctx)
		if err != nil {
			s.log.Warn("task step (dropoff) create failed", zap.Error(err))
		}
	}

	s.log.Info("task created",
		zap.String("task_id", t.ID.String()),
		zap.String("type", taskType),
		zap.String("ref", req.ExternalReference),
	)

	if s.publisher != nil {
		if pubErr := s.publisher.PublishTaskCreated(ctx, tenantID, events.TaskEventData{
			TaskID:            t.ID.String(),
			TrackingCode:      t.TrackingCode,
			ExternalReference: req.ExternalReference,
			Status:            t.Status,
			SourceService:     req.SourceService,
			CashOnDelivery:    t.CashOnDelivery,
		}); pubErr != nil {
			s.log.Warn("failed to publish task.created", zap.Error(pubErr), zap.String("task_id", t.ID.String()))
		}
	}

	return t, nil
}

// GetTask returns a task by ID, scoped to tenant.
func (s *Service) GetTask(ctx context.Context, tenantID, taskID uuid.UUID) (*ent.Task, error) {
	t, err := s.client.Task.Query().
		Where(task.ID(taskID), task.TenantID(tenantID)).
		WithAssignments().
		WithSteps().
		WithProofOfDelivery().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("tasks: not found")
		}
		return nil, fmt.Errorf("tasks: get: %w", err)
	}
	return t, nil
}

// ListTasks returns tasks for a tenant with optional filters.
func (s *Service) ListTasks(ctx context.Context, tenantID uuid.UUID, f ListTasksFilter) ([]*ent.Task, int, error) {
	q := s.client.Task.Query().
		Where(task.TenantID(tenantID)).
		WithAssignments().
		WithSteps()

	if len(f.Statuses) > 0 {
		q = q.Where(task.StatusIn(f.Statuses...))
	} else if f.Status != "" {
		q = q.Where(task.Status(f.Status))
	}

	if f.OutletID != nil {
		q = q.Where(task.OutletIDEQ(*f.OutletID))
	}

	if f.MemberID != uuid.Nil {
		q = q.Where(task.HasAssignmentsWith(taskassignment.FleetMemberID(f.MemberID)))
	}

	if f.Search != "" {
		q = q.Where(task.Or(
			task.TrackingCodeContainsFold(f.Search),
			task.ExternalReferenceContainsFold(f.Search),
		))
	}

	if f.DateFrom != nil {
		q = q.Where(task.CreatedAtGTE(*f.DateFrom))
	}
	if f.DateTo != nil {
		q = q.Where(task.CreatedAtLTE(*f.DateTo))
	}

	total, _ := q.Clone().Count(ctx)

	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	tasks, err := q.Limit(limit).Offset(f.Offset).Order(ent.Desc(task.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("tasks: list: %w", err)
	}
	return tasks, total, nil
}

// UpdateStatus transitions a task to a new status.
func (s *Service) UpdateStatus(ctx context.Context, tenantID, taskID uuid.UUID, newStatus string) (*ent.Task, error) {
	t, err := s.client.Task.Query().
		Where(task.ID(taskID), task.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("tasks: not found")
		}
		return nil, fmt.Errorf("tasks: query for status update: %w", err)
	}

	// Delivery is only ever recorded through proof of delivery (SubmitPoD), which checks the
	// customer's code and the cash collected. A plain status change to "delivered" skipped both.
	if newStatus == "delivered" || newStatus == "completed" {
		return nil, fmt.Errorf("tasks: submit proof of delivery to complete a delivery")
	}

	allowed, ok := validTransitions[t.Status]
	if !ok {
		return nil, fmt.Errorf("tasks: unknown current status %q", t.Status)
	}
	valid := false
	for _, s := range allowed {
		if s == newStatus {
			valid = true
			break
		}
	}
	if !valid {
		return nil, fmt.Errorf("tasks: invalid transition %q → %q", t.Status, newStatus)
	}

	if _, err := s.client.Task.UpdateOne(t).SetStatus(newStatus).Save(ctx); err != nil {
		return nil, fmt.Errorf("tasks: update status: %w", err)
	}
	updated, err := s.client.Task.Query().
		Where(task.ID(taskID)).
		WithSteps().
		WithAssignments().
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: re-fetch after status update: %w", err)
	}

	s.log.Info("task status updated",
		zap.String("task_id", taskID.String()),
		zap.String("from", t.Status),
		zap.String("to", newStatus),
	)

	// Publish task status change event
	if s.publisher != nil {
		_ = s.publisher.PublishTaskStatusChanged(ctx, tenantID, events.TaskEventData{
			TaskID:            taskID.String(),
			TrackingCode:      updated.TrackingCode,
			ExternalReference: updated.ExternalReference,
			Status:            newStatus,
			PreviousStatus:    t.Status,
			SourceService:     updated.SourceService,
			OrderNumber:       metadataString(updated.Metadata, "order_number"),
		})
	}

	// Trigger ETA recalculation when rider accepts or goes en-route (either leg) --
	// these are the transitions where location/route data becomes meaningful.
	if s.etaTrigger != nil && (newStatus == "accepted" || newStatus == "en_route" ||
		newStatus == "en_route_pickup" || newStatus == "en_route_dropoff") {
		go s.etaTrigger.ComputeAndPublishETA(ctx, tenantID, taskID)
	}

	// Broadcast status change to SSE subscribers (logistics-ui, public tracker).
	if s.sseBroadcaster != nil {
		s.sseBroadcaster.Publish(tenantID, taskID, "status_changed", map[string]any{
			"task_id":         taskID,
			"status":          newStatus,
			"previous_status": t.Status,
		})
	}

	return updated, nil
}

// AssignTask assigns a task to a fleet member.
func (s *Service) AssignTask(ctx context.Context, tenantID, taskID uuid.UUID, req AssignTaskRequest) (*ent.TaskAssignment, error) {
	// Verify member belongs to tenant
	member, err := s.client.FleetMember.Query().
		Where(fleetmember.ID(req.FleetMemberID), fleetmember.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("tasks: fleet member not found")
		}
		return nil, fmt.Errorf("tasks: verify member: %w", err)
	}

	// Check for existing active assignment
	existing, _ := s.client.TaskAssignment.Query().
		Where(
			taskassignment.TaskID(taskID),
			taskassignment.StatusIn("assigned", "accepted"),
		).
		First(ctx)
	if existing != nil {
		// Idempotent: if already assigned to the same member, return the existing assignment.
		if existing.FleetMemberID == req.FleetMemberID {
			return existing, nil
		}
		return nil, fmt.Errorf("tasks: task already assigned to an active member")
	}

	assignment, err := s.client.TaskAssignment.Create().
		SetTaskID(taskID).
		SetFleetMemberID(req.FleetMemberID).
		SetStatus("assigned").
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: create assignment: %w", err)
	}

	// Advance task status to "assigned"
	_, _ = s.client.Task.UpdateOneID(taskID).SetStatus("assigned").Save(ctx)

	s.log.Info("task assigned",
		zap.String("task_id", taskID.String()),
		zap.String("member_id", req.FleetMemberID.String()),
	)

	s.publishAssigned(ctx, tenantID, taskID, member)

	return assignment, nil
}

// publishAssigned publishes task.assigned enriched with the rider's contact (resolved from the
// local user table synced from auth) so notifications-api can alert the rider and ordering can
// show the customer who is bringing the order.
func (s *Service) publishAssigned(ctx context.Context, tenantID, taskID uuid.UUID, member *ent.FleetMember) {
	if s.publisher == nil || member == nil {
		return
	}
	t, _ := s.client.Task.Query().Where(task.ID(taskID)).WithSteps().Only(ctx)
	if t == nil {
		return
	}
	pickup := ""
	for _, st := range t.Edges.Steps {
		if st.StepType == "pickup" {
			pickup = st.LocationName
			if st.ContactName != "" {
				pickup = st.ContactName
			}
			break
		}
	}
	riderEmail, riderName := "", ""
	if ru, uerr := s.client.User.Query().Where(entuser.ID(member.UserID)).Only(ctx); uerr == nil && ru != nil {
		riderEmail = ru.Email
		riderName = ru.FullName
	}
	_ = s.publisher.PublishTaskAssigned(ctx, tenantID, events.TaskEventData{
		TaskID:            taskID.String(),
		TrackingCode:      t.TrackingCode,
		ExternalReference: t.ExternalReference,
		Status:            "assigned",
		FleetMemberID:     member.ID.String(),
		RiderEmail:        riderEmail,
		RiderName:         riderName,
		SourceService:     t.SourceService,
		OrderNumber:       metadataString(t.Metadata, "order_number"),
		RiderUserID:       member.UserID.String(),
		PickupName:        pickup,
	})
}

// SubmitPoD records proof of delivery and marks task as delivered.
func (s *Service) SubmitPoD(ctx context.Context, tenantID, taskID uuid.UUID, req SubmitPoDRequest) (*ent.ProofOfDelivery, error) {
	// Verify task belongs to tenant
	t, err := s.client.Task.Query().
		Where(task.ID(taskID), task.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("tasks: not found")
		}
		return nil, fmt.Errorf("tasks: get for pod: %w", err)
	}

	if !podAllowedFrom[t.Status] {
		return nil, fmt.Errorf("tasks: the order has not been picked up yet (status %s)", t.Status)
	}

	// Validate COD: the customer pays the rider in cash or by M-Pesa to the business, and the
	// full amount must be taken. An M-Pesa collection needs its confirmation code so the
	// business can match it on its statement.
	codAmount := taskCODAmount(t)
	method := strings.ToLower(strings.TrimSpace(req.CollectionMethod))
	reference := strings.ToUpper(strings.Join(strings.Fields(req.CollectionReference), ""))
	if codAmount > 0 {
		if req.AmountCollected < codAmount {
			return nil, fmt.Errorf("tasks: amount collected (%.2f) is less than the %.2f due", req.AmountCollected, codAmount)
		}
		if method == "" {
			method = "cash"
		}
		if method != "cash" && method != "mpesa" {
			return nil, fmt.Errorf("tasks: collection method must be cash or mpesa")
		}
		if method == "mpesa" && len(reference) != 10 {
			return nil, fmt.Errorf("tasks: enter the 10-character M-Pesa code for this payment")
		}
	}

	// Validate proof-of-delivery confirmation code. ordering-backend stores a
	// 6-digit code in the task metadata under "pod_code"; the customer relays
	// it to the rider on delivery. When present and non-empty it MUST match the
	// confirmation_code supplied by the rider. Tasks without a pod_code (legacy
	// or non-delivery tasks) remain fully backward-compatible.
	if t.Metadata != nil {
		if raw, ok := t.Metadata["pod_code"]; ok {
			if podCode, ok := raw.(string); ok && podCode != "" {
				if req.ConfirmationCode == "" {
					return nil, fmt.Errorf("tasks: delivery confirmation code required")
				}
				if req.ConfirmationCode != podCode {
					return nil, fmt.Errorf("tasks: incorrect delivery confirmation code")
				}
			}
		}
	}

	meta := req.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	if reference != "" {
		meta["collection_reference"] = reference
	}
	if req.RecipientName != "" {
		meta["recipient_name"] = req.RecipientName
	}
	if req.Notes != "" {
		meta["notes"] = req.Notes
	}
	if req.Latitude != nil && req.Longitude != nil {
		meta["latitude"] = *req.Latitude
		meta["longitude"] = *req.Longitude
	}

	// Resolve the fleet member from the task's active assignment rather than
	// trusting the client-supplied req.FleetMemberID (the rider-app PoD payload
	// does not send it, so it arrives as uuid.Nil). The assignment-derived id
	// always wins when present; req.FleetMemberID is only a fallback.
	memberID := req.FleetMemberID
	if activeAssignments, aerr := s.client.TaskAssignment.Query().
		Where(
			taskassignment.TaskID(taskID),
			taskassignment.StatusIn("assigned", "accepted"),
		).
		All(ctx); aerr == nil && len(activeAssignments) > 0 {
		memberID = activeAssignments[0].FleetMemberID
	}

	builder := s.client.ProofOfDelivery.Create().
		SetTenantID(tenantID).
		SetTaskID(taskID).
		SetFleetMemberID(memberID).
		SetCapturedAt(time.Now()).
		SetAmountCollected(req.AmountCollected).
		SetMetadata(meta)

	if req.SignatureURL != "" {
		builder.SetSignatureURL(req.SignatureURL)
	}
	if req.PhotoURL != "" {
		builder.SetPhotoURL(req.PhotoURL)
	}
	if req.OTPCode != "" {
		builder.SetOtpCode(req.OTPCode)
	}
	if method != "" {
		builder.SetCollectionMethod(method)
	}

	pod, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: create pod: %w", err)
	}

	// Transition task to delivered. Guarded to only move the task forward (never
	// resurrect an already-terminal task) rather than allow-listing specific
	// pre-delivery statuses -- that allow-list previously named only "en_route" and
	// "accepted", so a real rider on the granular per-leg flow (e.g. "arrived_dropoff")
	// got a ProofOfDelivery record and a logistics.task.completed event published, but
	// the task row itself silently never flipped to "delivered" and stayed stuck on
	// the rider's active-deliveries list forever.
	taskUpdate := s.client.Task.UpdateOneID(taskID).SetStatus("delivered")
	if codAmount > 0 && req.AmountCollected >= codAmount {
		taskUpdate.SetCashCollected(true)
	}
	if t.Status != "delivered" && t.Status != "completed" && t.Status != "cancelled" && t.Status != "failed" {
		_, _ = taskUpdate.Save(ctx)
	}

	// Mark assignment as completed
	assignments, _ := s.client.TaskAssignment.Query().
		Where(
			taskassignment.TaskID(taskID),
			taskassignment.FleetMemberID(memberID),
			taskassignment.StatusIn("assigned", "accepted"),
		).
		All(ctx)
	now := time.Now()
	for _, a := range assignments {
		_, _ = s.client.TaskAssignment.UpdateOne(a).
			SetStatus("completed").
			SetCompletedAt(now).
			Save(ctx)
	}

	s.log.Info("proof of delivery submitted",
		zap.String("task_id", taskID.String()),
		zap.String("pod_id", pod.ID.String()),
	)

	// Publish task completed event
	if s.publisher != nil {
		_ = s.publisher.PublishTaskCompleted(ctx, tenantID, events.TaskEventData{
			TaskID:              taskID.String(),
			TrackingCode:        t.TrackingCode,
			ExternalReference:   t.ExternalReference,
			Status:              "delivered",
			FleetMemberID:       memberID.String(),
			SourceService:       t.SourceService,
			CashOnDelivery:      codAmount,
			CashCollected:       codAmount > 0 && req.AmountCollected >= codAmount,
			AmountCollected:     req.AmountCollected,
			OrderNumber:         metadataString(t.Metadata, "order_number"),
			CollectionMethod:    method,
			CollectionReference: reference,
		})
	}

	// Record rider earning asynchronously, attributed to the resolved member.
	if s.earningsSvc != nil {
		// Prefer the task's actual delivery fee (set by ordering-backend in the
		// task metadata) as the rider earning. Fall back to the distance-based
		// PricingRule path for legacy tasks that carry no delivery_fee.
		var deliveryFee float64
		if t.Metadata != nil {
			if raw, ok := t.Metadata["delivery_fee"]; ok {
				deliveryFee = metadataNumber(raw)
			}
		}
		earnMemberID := memberID
		go func() {
			earnCtx := context.Background()
			if deliveryFee > 0 {
				if earnErr := s.earningsSvc.RecordEarningWithAmount(earnCtx, tenantID, taskID, earnMemberID, deliveryFee); earnErr != nil {
					s.log.Warn("failed to record delivery earning", zap.Error(earnErr))
				}
				return
			}
			// TODO: Calculate actual distance from task steps once routing is integrated
			var distanceKm float64 = 5.0 // default fallback
			if earnErr := s.earningsSvc.RecordEarning(earnCtx, tenantID, taskID, earnMemberID, distanceKm); earnErr != nil {
				s.log.Warn("failed to record delivery earning", zap.Error(earnErr))
			}
		}()
	}

	return pod, nil
}

// CreateTaskFromOrder creates a delivery task from an ordering event, including
// pickup/dropoff TaskSteps with coordinates for auto-dispatch.
func (s *Service) CreateTaskFromOrder(ctx context.Context, tenantID uuid.UUID, externalRef string, req CreateTaskFromOrderRequest) (*ent.Task, error) {
	// Idempotent: check if task already exists for this order
	existing, err := s.client.Task.Query().
		Where(task.TenantID(tenantID), task.ExternalReference(externalRef)).
		WithSteps().
		First(ctx)
	if err == nil {
		return existing, nil
	}

	// Get tenant's fleet (auto-create if needed)
	fl, err := s.getOrCreateDefaultFleet(ctx, tenantID)
	if err != nil {
		s.log.Warn("could not resolve fleet for task", zap.Error(err))
	}
	_ = fl

	// Build metadata with order context
	metadata := map[string]any{
		"order_id":     req.OrderID,
		"order_number": req.OrderNumber,
	}
	if req.CashOnDelivery > 0 {
		metadata["cash_on_delivery"] = req.CashOnDelivery
		metadata["payment_method"] = "cod"
	} else if req.PaymentMethod != "" {
		metadata["payment_method"] = req.PaymentMethod
	}
	if req.FulfillmentType != "" {
		metadata["fulfillment_type"] = req.FulfillmentType
	}
	if req.PODCode != "" {
		metadata["pod_code"] = req.PODCode
	}
	if req.DeliveryFee > 0 {
		metadata["delivery_fee"] = req.DeliveryFee
	}
	if req.OutletID != "" {
		metadata["outlet_id"] = req.OutletID
	}
	if req.PickupPhone != "" {
		metadata["pickup_phone"] = req.PickupPhone
	}
	// What is in the bag, so the rider can check it at the counter.
	if desc, count := describeItems(req.Items); count > 0 {
		metadata["items_description"] = desc
		metadata["item_count"] = count
	}

	// Store pickup/dropoff coords in metadata as fallback for dispatcher
	if req.PickupLat != 0 && req.PickupLng != 0 {
		metadata["pickup_lat"] = req.PickupLat
		metadata["pickup_lng"] = req.PickupLng
	}

	// Create the task
	t, err := s.CreateTask(ctx, tenantID, CreateTaskRequest{
		ExternalReference: externalRef,
		SourceService:     "ordering",
		TaskType:          "delivery",
		Metadata:          metadata,
	})
	if err != nil {
		return nil, err
	}
	// Scope the task to its outlet so a branch dispatcher filtering by outlet sees it.
	if oid, perr := uuid.Parse(req.OutletID); perr == nil {
		if upd, uerr := s.client.Task.UpdateOneID(t.ID).SetOutletID(oid).Save(ctx); uerr == nil {
			t = upd
		}
	}

	// Create pickup step (sequence 1)
	if req.PickupLat != 0 && req.PickupLng != 0 {
		_, stepErr := s.client.TaskStep.Create().
			SetTaskID(t.ID).
			SetStepType("pickup").
			SetSequence(1).
			SetLocationName(req.PickupName).
			SetContactName(req.PickupName).
			SetContactPhone(req.PickupPhone).
			SetAddressJSON(map[string]any{
				"latitude":  req.PickupLat,
				"longitude": req.PickupLng,
				"name":      req.PickupName,
				"address":   req.PickupAddress,
			}).
			SetMetadata(map[string]any{
				"order_number": req.OrderNumber,
			}).
			Save(ctx)
		if stepErr != nil {
			s.log.Warn("failed to create pickup step", zap.Error(stepErr))
		}
	}

	// Create dropoff step (sequence 2)
	if req.DropoffLat != 0 && req.DropoffLng != 0 {
		_, stepErr := s.client.TaskStep.Create().
			SetTaskID(t.ID).
			SetStepType("dropoff").
			SetSequence(2).
			SetLocationName(req.DropoffName).
			SetContactName(req.CustomerName).
			SetContactPhone(req.CustomerPhone).
			SetAddressJSON(map[string]any{
				"latitude":  req.DropoffLat,
				"longitude": req.DropoffLng,
				"name":      req.DropoffName,
			}).
			SetMetadata(map[string]any{
				"instructions": req.Instructions,
			}).
			Save(ctx)
		if stepErr != nil {
			s.log.Warn("failed to create dropoff step", zap.Error(stepErr))
		}
	}

	// Re-fetch task with steps for auto-dispatch
	t, err = s.client.Task.Query().
		Where(task.ID(t.ID)).
		WithSteps().
		Only(ctx)
	if err != nil {
		s.log.Warn("failed to re-fetch task with steps", zap.Error(err))
	}

	return t, nil
}

func (s *Service) getOrCreateDefaultFleet(ctx context.Context, tenantID uuid.UUID) (*ent.Fleet, error) {
	fl, err := s.client.Fleet.Query().
		Where(fleet.TenantID(tenantID), fleet.Status("active")).
		First(ctx)
	if err == nil {
		return fl, nil
	}
	return s.client.Fleet.Create().
		SetTenantID(tenantID).
		SetTenantSlug(tenantID.String()).
		SetName("Default Fleet").
		SetType("internal").
		SetStatus("active").
		Save(ctx)
}

// GetTaskByTrackingCode looks up a task by its public tracking code (no tenant scoping).
func (s *Service) GetTaskByTrackingCode(ctx context.Context, code string) (*ent.Task, error) {
	t, err := s.client.Task.Query().
		Where(task.TrackingCode(code)).
		WithAssignments(func(q *ent.TaskAssignmentQuery) {
			q.Order(ent.Desc(taskassignment.FieldAssignedAt)).Limit(1)
		}).
		WithSteps().
		WithEvents().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("tasks: tracking code not found")
		}
		return nil, fmt.Errorf("tasks: tracking lookup: %w", err)
	}
	return t, nil
}

// generateTrackingCode creates a unique tracking code in format CV-YYYYMMDD-XXXXXX.
func generateTrackingCode() string {
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // No 0/O/1/I for readability
	now := time.Now()
	dateStr := now.Format("20060102")

	var b strings.Builder
	b.WriteString("CV-")
	b.WriteString(dateStr)
	b.WriteRune('-')

	for i := 0; i < 6; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b.WriteByte(charset[n.Int64()])
	}

	return b.String()
}
