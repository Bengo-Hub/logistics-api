package tasks

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleet"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	"github.com/bengobox/logistics-service/internal/platform/events"
)

// Valid task statuses (state machine).
var validTransitions = map[string][]string{
	"pending":          {"assigned", "cancelled"},
	"assigned":         {"accepted", "cancelled"},
	"accepted":         {"en_route", "cancelled"},
	"en_route":         {"delivered", "failed"},
	"delivered":        {},
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
}

// AssignTaskRequest is the DTO for assigning a task to a fleet member.
type AssignTaskRequest struct {
	FleetMemberID uuid.UUID `json:"fleet_member_id"`
}

// SubmitPoDRequest is the DTO for submitting proof of delivery.
type SubmitPoDRequest struct {
	FleetMemberID    uuid.UUID      `json:"fleet_member_id"`
	SignatureURL     string         `json:"signature_url,omitempty"`
	PhotoURL         string         `json:"photo_url,omitempty"`
	OTPCode          string         `json:"otp_code,omitempty"`
	AmountCollected  float64        `json:"amount_collected,omitempty"`
	CollectionMethod string         `json:"collection_method,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

// ListTasksFilter holds optional filters for listing tasks.
type ListTasksFilter struct {
	Status    string
	MemberID  uuid.UUID
	Limit     int
	Offset    int
}

// EarningsRecorder is the interface for recording delivery earnings.
type EarningsRecorder interface {
	RecordEarning(ctx context.Context, tenantID, taskID, memberID uuid.UUID, distanceKm float64) error
}

// Service handles task business logic.
type Service struct {
	client      *ent.Client
	log         *zap.Logger
	publisher   *events.Publisher
	earningsSvc EarningsRecorder
}

// NewService creates a new task service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("tasks.service"),
	}
}

// SetEarningsService sets the earnings service for recording delivery earnings.
func (s *Service) SetEarningsService(svc EarningsRecorder) {
	s.earningsSvc = svc
}

// SetPublisher sets the event publisher for task lifecycle events.
func (s *Service) SetPublisher(p *events.Publisher) {
	s.publisher = p
}

// CreateTask creates a new delivery task.
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
	if req.Metadata != nil {
		builder.SetMetadata(req.Metadata)
	}

	t, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: create: %w", err)
	}

	s.log.Info("task created",
		zap.String("task_id", t.ID.String()),
		zap.String("type", taskType),
		zap.String("ref", req.ExternalReference),
	)
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
func (s *Service) ListTasks(ctx context.Context, tenantID uuid.UUID, f ListTasksFilter) ([]*ent.Task, error) {
	q := s.client.Task.Query().
		Where(task.TenantID(tenantID)).
		WithAssignments()

	if f.Status != "" {
		q = q.Where(task.Status(f.Status))
	}

	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q = q.Limit(limit).Offset(f.Offset).Order(ent.Desc(task.FieldCreatedAt))

	tasks, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: list: %w", err)
	}
	return tasks, nil
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

	updated, err := s.client.Task.UpdateOne(t).SetStatus(newStatus).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: update status: %w", err)
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
		})
	}

	return updated, nil
}

// AssignTask assigns a task to a fleet member.
func (s *Service) AssignTask(ctx context.Context, tenantID, taskID uuid.UUID, req AssignTaskRequest) (*ent.TaskAssignment, error) {
	// Verify member belongs to tenant
	_, err := s.client.FleetMember.Query().
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

	// Publish task assigned event
	if s.publisher != nil {
		t, _ := s.client.Task.Query().Where(task.ID(taskID)).Only(ctx)
		if t != nil {
			_ = s.publisher.PublishTaskAssigned(ctx, tenantID, events.TaskEventData{
				TaskID:            taskID.String(),
				TrackingCode:      t.TrackingCode,
				ExternalReference: t.ExternalReference,
				Status:            "assigned",
				FleetMemberID:     req.FleetMemberID.String(),
				SourceService:     t.SourceService,
			})
		}
	}

	return assignment, nil
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

	// Validate COD: if task requires cash collection, ensure amount was collected
	if t.CashOnDelivery > 0 && req.AmountCollected < t.CashOnDelivery {
		return nil, fmt.Errorf("tasks: COD amount collected (%.2f) is less than required (%.2f)", req.AmountCollected, t.CashOnDelivery)
	}

	meta := req.Metadata
	if meta == nil {
		meta = map[string]any{}
	}

	builder := s.client.ProofOfDelivery.Create().
		SetTenantID(tenantID).
		SetTaskID(taskID).
		SetFleetMemberID(req.FleetMemberID).
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
	if req.CollectionMethod != "" {
		builder.SetCollectionMethod(req.CollectionMethod)
	}

	pod, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: create pod: %w", err)
	}

	// Transition task to delivered
	taskUpdate := s.client.Task.UpdateOneID(taskID).SetStatus("delivered")
	if t.CashOnDelivery > 0 && req.AmountCollected >= t.CashOnDelivery {
		taskUpdate.SetCashCollected(true)
	}
	if t.Status == "en_route" || t.Status == "accepted" {
		_, _ = taskUpdate.Save(ctx)
	}

	// Mark assignment as completed
	assignments, _ := s.client.TaskAssignment.Query().
		Where(
			taskassignment.TaskID(taskID),
			taskassignment.FleetMemberID(req.FleetMemberID),
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
			TaskID:            taskID.String(),
			TrackingCode:      t.TrackingCode,
			ExternalReference: t.ExternalReference,
			Status:            "delivered",
			FleetMemberID:     req.FleetMemberID.String(),
			SourceService:     t.SourceService,
			CashOnDelivery:    t.CashOnDelivery,
			CashCollected:     t.CashOnDelivery > 0 && req.AmountCollected >= t.CashOnDelivery,
			AmountCollected:   req.AmountCollected,
		})
	}

	// Record rider earning asynchronously
	if s.earningsSvc != nil {
		go func() {
			earnCtx := context.Background()
			// TODO: Calculate actual distance from task steps once routing is integrated
			var distanceKm float64 = 5.0 // default fallback
			if earnErr := s.earningsSvc.RecordEarning(earnCtx, tenantID, taskID, req.FleetMemberID, distanceKm); earnErr != nil {
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
	}
	if req.FulfillmentType != "" {
		metadata["fulfillment_type"] = req.FulfillmentType
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

	// Create pickup step (sequence 1)
	if req.PickupLat != 0 && req.PickupLng != 0 {
		_, stepErr := s.client.TaskStep.Create().
			SetTaskID(t.ID).
			SetStepType("pickup").
			SetSequence(1).
			SetLocationName(req.PickupName).
			SetAddressJSON(map[string]any{
				"latitude":  req.PickupLat,
				"longitude": req.PickupLng,
				"name":      req.PickupName,
			}).
			SetMetadata(map[string]any{}).
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
