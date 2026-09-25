package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/predicate"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
)

// ConfigKeyRiderSelfClaim lets riders pick up unassigned delivery jobs themselves (a small
// restaurant with its own riders and no dispatcher). On by default; a business that wants every
// job assigned by its dispatcher turns it off.
const ConfigKeyRiderSelfClaim = "logistics.rider_self_claim_enabled"

var (
	// ErrSelfClaimDisabled means the business assigns every job from its dispatch board.
	ErrSelfClaimDisabled = errors.New("tasks: jobs are assigned by your dispatcher")
	// ErrTaskTaken means another rider (or the dispatcher) got the job first.
	ErrTaskTaken = errors.New("tasks: this job has already been taken")
	// ErrRiderNotActive means the rider's fleet membership is not active (invited, suspended...).
	ErrRiderNotActive = errors.New("tasks: your rider account is not active")
)

// RiderSelfClaimEnabled reports whether riders of this tenant may claim open jobs.
func (s *Service) RiderSelfClaimEnabled(ctx context.Context, tenantID uuid.UUID) bool {
	return s.boolSetting(ctx, tenantID, ConfigKeyRiderSelfClaim, true)
}

// openTaskPredicates match jobs no rider holds: still pending and without an active assignment.
func openTaskPredicates(tenantID uuid.UUID) []predicate.Task {
	return []predicate.Task{
		task.TenantID(tenantID),
		task.StatusEQ("pending"),
		task.Not(task.HasAssignmentsWith(taskassignment.StatusIn("assigned", "accepted"))),
	}
}

// ListOpenTasks returns the jobs riders can claim, oldest first so food does not sit waiting.
func (s *Service) ListOpenTasks(ctx context.Context, tenantID uuid.UUID, limit int) ([]*ent.Task, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	list, err := s.client.Task.Query().
		Where(openTaskPredicates(tenantID)...).
		WithSteps().
		Order(ent.Asc(task.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: list open: %w", err)
	}
	return list, nil
}

// ClaimTask gives an open job to the rider who asked for it. The pending-to-assigned change is a
// single conditional update, so when two riders tap the same job only one wins; the other gets
// ErrTaskTaken. Claiming counts as accepting the job.
func (s *Service) ClaimTask(ctx context.Context, tenantID, taskID, memberID uuid.UUID) (*ent.Task, error) {
	if !s.RiderSelfClaimEnabled(ctx, tenantID) {
		return nil, ErrSelfClaimDisabled
	}
	member, err := s.client.FleetMember.Query().
		Where(fleetmember.ID(memberID), fleetmember.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrRiderNotActive
		}
		return nil, fmt.Errorf("tasks: load rider: %w", err)
	}
	if member.Status != "active" {
		return nil, ErrRiderNotActive
	}

	won, err := s.client.Task.Update().
		Where(append(openTaskPredicates(tenantID), task.ID(taskID))...).
		SetStatus("assigned").
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("tasks: claim: %w", err)
	}
	if won == 0 {
		return nil, ErrTaskTaken
	}

	if _, err := s.client.TaskAssignment.Create().
		SetTaskID(taskID).
		SetFleetMemberID(memberID).
		SetStatus("assigned").
		SetMetadata(map[string]any{"claimed_by_rider": true}).
		Save(ctx); err != nil {
		// Give the job back so it does not sit "assigned" to nobody.
		_, _ = s.client.Task.UpdateOneID(taskID).SetStatus("pending").Save(ctx)
		return nil, fmt.Errorf("tasks: record claim: %w", err)
	}
	s.log.Info("task claimed by rider",
		zap.String("task_id", taskID.String()),
		zap.String("member_id", memberID.String()))

	s.publishAssigned(ctx, tenantID, taskID, member)
	return s.UpdateStatus(ctx, tenantID, taskID, "accepted")
}
