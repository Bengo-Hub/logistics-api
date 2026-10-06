package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	notifmod "github.com/bengobox/logistics-service/internal/modules/notifications"
	"github.com/bengobox/logistics-service/internal/platform/events"
)

var (
	// ErrNotYourTask means the rider does not hold the task they tried to act on.
	ErrNotYourTask = errors.New("tasks: this delivery is assigned to another rider")
	// ErrAlreadyPickedUp means the order has left the outlet, so the job can no longer be handed
	// to another rider; the rider completes it or reports a failed delivery.
	ErrAlreadyPickedUp = errors.New("tasks: the order has already been picked up; complete it or report a failed delivery")
	// ErrTaskClosed means the task is already delivered, failed or cancelled.
	ErrTaskClosed = errors.New("tasks: this delivery is already closed")
	// ErrTaskChanged means the task moved on while the request was in flight (another rider,
	// the dispatcher or ordering changed it first).
	ErrTaskChanged = errors.New("tasks: this delivery was just updated by someone else; refresh and try again")
	// ErrReasonRequired means a reason must be given (failed delivery, cancellation, decline).
	ErrReasonRequired = errors.New("tasks: please give a reason")
	// ErrRiderMayNotSetStatus means a rider tried a status only a dispatcher may set.
	ErrRiderMayNotSetStatus = errors.New("tasks: riders cannot set this status; decline the job or report a failed delivery instead")
)

// Actor identifies who changed a task, for the task's history.
type Actor struct {
	ID   uuid.UUID
	Type string // rider | dispatcher | system | ordering | pos
}

// SystemActor is used for automatic changes (auto-dispatch, event consumers).
var SystemActor = Actor{Type: "system"}

// SetNotifications wires the dispatcher alert feed so riders declining or failing a job, and
// orders cancelled while a rider holds them, reach the dispatch board.
func (s *Service) SetNotifications(n *notifmod.Service) { s.notifSvc = n }

// SetRedispatcher wires auto-dispatch so a declined job is offered to the next rider straight
// away when the tenant has auto-assign on. It lives behind a function because the dispatch
// package imports this one.
func (s *Service) SetRedispatcher(fn func(ctx context.Context, tenantID, taskID uuid.UUID)) {
	s.redispatch = fn
}

// recordEvent appends a row to the task's history (public tracking timeline, picked-up and
// cancelled timestamps). Best effort: history must never block the change itself.
func (s *Service) recordEvent(ctx context.Context, taskID uuid.UUID, eventType string, actor Actor, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	c := s.client.TaskEvent.Create().
		SetTaskID(taskID).
		SetEventType(eventType).
		SetPayload(payload).
		SetOccurredAt(time.Now().UTC())
	if actor.ID != uuid.Nil {
		c.SetActorID(actor.ID)
	}
	if actor.Type != "" {
		c.SetActorType(actor.Type)
	}
	if _, err := c.Save(ctx); err != nil {
		s.log.Warn("task history write failed", zap.String("task_id", taskID.String()), zap.String("event", eventType), zap.Error(err))
	}
}

// statusChangeError checks a requested status change before anything is written. byRider is
// true when the caller is the rider holding the task (not a dispatcher or a service).
func statusChangeError(from, to string, byRider bool, reason string) error {
	if to == "delivered" || to == "completed" {
		return fmt.Errorf("tasks: submit proof of delivery to complete a delivery")
	}
	if IsTerminal(from) {
		return ErrTaskClosed
	}
	if byRider && !riderStatusAllowed(to) {
		return ErrRiderMayNotSetStatus
	}
	if (to == "failed" || to == "cancelled") && strings.TrimSpace(reason) == "" {
		return ErrReasonRequired
	}
	if !validTransition(from, to) {
		return fmt.Errorf("tasks: invalid transition %q to %q", from, to)
	}
	return nil
}

// reasonMetadata returns the metadata keys a status change stamps on the task, so boards and
// ordering can show why a delivery failed or was cancelled and when the order left the outlet.
func reasonMetadata(to, reason string, at time.Time) map[string]any {
	stamp := at.UTC().Format(time.RFC3339)
	out := map[string]any{}
	switch to {
	case "failed":
		out["failure_reason"] = reason
		out["failed_at"] = stamp
	case "cancelled":
		out["cancellation_reason"] = reason
		out["cancelled_at"] = stamp
	case "picked_up", "en_route":
		out["picked_up_at"] = stamp
	}
	return out
}

func mergeMeta(base map[string]any, add map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(add))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range add {
		out[k] = v
	}
	return out
}

// closeActiveAssignments ends the rider's hold on a task (failed, cancelled, declined,
// unassigned), which frees the rider for other jobs.
func (s *Service) closeActiveAssignments(ctx context.Context, client *ent.Client, taskID uuid.UUID, status, reason string) (uuid.UUID, error) {
	active, err := client.TaskAssignment.Query().
		Where(taskassignment.TaskID(taskID), taskassignment.StatusIn(activeAssignmentStatuses...)).
		All(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	now := time.Now().UTC()
	var member uuid.UUID
	for _, a := range active {
		member = a.FleetMemberID
		upd := client.TaskAssignment.UpdateOne(a).SetStatus(status)
		if status == "declined" || status == "unassigned" {
			upd.SetDeclinedAt(now)
		} else {
			upd.SetCompletedAt(now)
		}
		if reason != "" {
			upd.SetReasonCode(truncate(reason, 250))
		}
		if _, err := upd.Save(ctx); err != nil {
			return member, err
		}
	}
	return member, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// DeclineTask lets the rider holding a job hand it back before collecting the order (bike
// trouble, too far, shift over). The job returns to the open pool and, when the business uses
// auto-assign, goes to the next nearest rider; that rider never gets it offered back.
func (s *Service) DeclineTask(ctx context.Context, tenantID, taskID, memberID uuid.UUID, reason string) (*ent.Task, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrReasonRequired
	}
	holder, ok := s.ActiveAssignee(ctx, taskID)
	if !ok || holder != memberID {
		return nil, ErrNotYourTask
	}
	return s.releaseTask(ctx, tenantID, taskID, "declined", reason, Actor{ID: memberID, Type: "rider"}, true)
}

// UnassignTask lets a dispatcher take a job back from its rider before pickup (to give it to
// someone else or hold it). It does not re-dispatch on its own.
func (s *Service) UnassignTask(ctx context.Context, tenantID, taskID uuid.UUID, reason string, actor Actor) (*ent.Task, error) {
	if strings.TrimSpace(reason) == "" {
		reason = "unassigned by dispatcher"
	}
	return s.releaseTask(ctx, tenantID, taskID, "unassigned", reason, actor, false)
}

// releaseTask returns a pre-pickup task to pending and ends the current rider's assignment in
// one transaction.
func (s *Service) releaseTask(ctx context.Context, tenantID, taskID uuid.UUID, assignmentStatus, reason string, actor Actor, redispatch bool) (*ent.Task, error) {
	t, err := s.client.Task.Query().Where(task.ID(taskID), task.TenantID(tenantID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("tasks: not found")
		}
		return nil, fmt.Errorf("tasks: load: %w", err)
	}
	if IsTerminal(t.Status) {
		return nil, ErrTaskClosed
	}
	if !IsPrePickup(t.Status) {
		if t.Status == "pending" {
			return nil, fmt.Errorf("tasks: no rider holds this delivery")
		}
		return nil, ErrAlreadyPickedUp
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: begin: %w", err)
	}
	n, err := tx.Task.Update().
		Where(task.ID(taskID), task.TenantID(tenantID), task.StatusEQ(t.Status)).
		SetStatus("pending").
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("tasks: release: %w", err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return nil, ErrTaskChanged
	}
	previous, err := s.closeActiveAssignments(ctx, tx.Client(), taskID, assignmentStatus, reason)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("tasks: end assignment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("tasks: commit release: %w", err)
	}

	s.log.Info("task released",
		zap.String("task_id", taskID.String()),
		zap.String("by", actor.Type),
		zap.String("as", assignmentStatus))
	s.recordEvent(ctx, taskID, assignmentStatus, actor, map[string]any{
		"reason": reason, "previous_status": t.Status, "fleet_member_id": previous.String(),
	})
	if s.publisher != nil {
		_ = s.publisher.PublishTaskUnassigned(ctx, tenantID, events.TaskEventData{
			TaskID:            taskID.String(),
			TrackingCode:      t.TrackingCode,
			ExternalReference: t.ExternalReference,
			Status:            "pending",
			PreviousStatus:    t.Status,
			FleetMemberID:     previous.String(),
			SourceService:     t.SourceService,
			OrderNumber:       metadataString(t.Metadata, "order_number"),
			Reason:            reason,
		})
	}
	if s.sseBroadcaster != nil {
		s.sseBroadcaster.Publish(tenantID, taskID, "status_changed", map[string]any{
			"task_id": taskID, "status": "pending", "previous_status": t.Status,
		})
	}
	if assignmentStatus == "declined" {
		s.alertDispatcher(ctx, tenantID, taskID, "rider_declined",
			"Rider declined a delivery",
			fmt.Sprintf("Order %s needs a rider: %s", orderLabel(t), reason))
	}
	if redispatch && s.redispatch != nil && s.AutoAssignEnabled(ctx, tenantID) {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		go func() {
			defer cancel()
			s.redispatch(dctx, tenantID, taskID)
		}()
	}
	return s.client.Task.Query().Where(task.ID(taskID)).WithSteps().WithAssignments().Only(ctx)
}

// CancelTask closes a task that will not be delivered (the customer or outlet cancelled the
// order, or a dispatcher cancels it). The rider is freed. When the rider had already collected
// the order, the dispatcher is told so the food or goods come back to the outlet.
func (s *Service) CancelTask(ctx context.Context, tenantID, taskID uuid.UUID, reason string, actor Actor) (*ent.Task, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, ErrReasonRequired
	}
	t, err := s.client.Task.Query().Where(task.ID(taskID), task.TenantID(tenantID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("tasks: not found")
		}
		return nil, fmt.Errorf("tasks: load: %w", err)
	}
	if IsTerminal(t.Status) {
		return t, ErrTaskClosed
	}
	now := time.Now().UTC()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: begin: %w", err)
	}
	n, err := tx.Task.Update().
		Where(task.ID(taskID), task.TenantID(tenantID), task.StatusEQ(t.Status)).
		SetStatus("cancelled").
		SetMetadata(mergeMeta(t.Metadata, reasonMetadata("cancelled", reason, now))).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("tasks: cancel: %w", err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return nil, ErrTaskChanged
	}
	rider, err := s.closeActiveAssignments(ctx, tx.Client(), taskID, "cancelled", reason)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("tasks: end assignment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("tasks: commit cancel: %w", err)
	}

	s.recordEvent(ctx, taskID, "cancelled", actor, map[string]any{"reason": reason, "previous_status": t.Status})
	if s.publisher != nil {
		data := events.TaskEventData{
			TaskID:            taskID.String(),
			TrackingCode:      t.TrackingCode,
			ExternalReference: t.ExternalReference,
			Status:            "cancelled",
			PreviousStatus:    t.Status,
			SourceService:     t.SourceService,
			OrderNumber:       metadataString(t.Metadata, "order_number"),
			Reason:            reason,
		}
		if rider != uuid.Nil {
			data.FleetMemberID = rider.String()
		}
		_ = s.publisher.PublishTaskStatusChanged(ctx, tenantID, data)
	}
	if s.sseBroadcaster != nil {
		s.sseBroadcaster.Publish(tenantID, taskID, "status_changed", map[string]any{
			"task_id": taskID, "status": "cancelled", "previous_status": t.Status,
		})
	}
	if rider != uuid.Nil && !IsPrePickup(t.Status) {
		s.alertDispatcher(ctx, tenantID, taskID, "cancelled_in_transit",
			"Cancelled order is with a rider",
			fmt.Sprintf("Order %s was cancelled after pickup (%s). Ask the rider to bring it back.", orderLabel(t), reason))
	}
	return s.client.Task.Query().Where(task.ID(taskID)).WithSteps().WithAssignments().Only(ctx)
}

// FindTaskByReference returns the newest task for an upstream reference (for example
// "order:<uuid>"), or nil when there is none.
func (s *Service) FindTaskByReference(ctx context.Context, tenantID uuid.UUID, ref string) (*ent.Task, error) {
	t, err := s.client.Task.Query().
		Where(task.TenantID(tenantID), task.ExternalReference(ref)).
		Order(ent.Desc(task.FieldCreatedAt)).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return t, err
}

func orderLabel(t *ent.Task) string {
	if n := metadataString(t.Metadata, "order_number"); n != "" {
		return n
	}
	if t.TrackingCode != "" {
		return t.TrackingCode
	}
	return t.ID.String()[:8]
}

// alertDispatcher raises a dispatch-board notification (one unread alert per task and kind).
func (s *Service) alertDispatcher(ctx context.Context, tenantID, taskID uuid.UUID, kind, title, body string) {
	if s.notifSvc == nil {
		return
	}
	id := taskID
	if _, err := s.notifSvc.CreateDeduped(ctx, notifmod.CreateRequest{
		TenantID:         tenantID,
		NotificationType: kind,
		Title:            title,
		Body:             body,
		Payload:          map[string]any{"task_id": taskID.String()},
		RelatedTaskID:    &id,
	}); err != nil {
		s.log.Warn("dispatcher alert failed", zap.String("task_id", taskID.String()), zap.Error(err))
	}
}

// DeclinedMembers returns the riders who declined or were taken off a task, so auto-dispatch
// does not offer it back to them.
func (s *Service) DeclinedMembers(ctx context.Context, taskID uuid.UUID) map[uuid.UUID]bool {
	out := map[uuid.UUID]bool{}
	rows, err := s.client.TaskAssignment.Query().
		Where(taskassignment.TaskID(taskID), taskassignment.StatusIn("declined", "unassigned")).
		All(ctx)
	if err != nil {
		return out
	}
	for _, a := range rows {
		out[a.FleetMemberID] = true
	}
	return out
}
