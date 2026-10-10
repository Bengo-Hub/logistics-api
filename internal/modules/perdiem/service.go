// Package perdiem raises per diem claims in erp-api for staff riders. erp-api owns the
// policy (rates, job group rates, minimum distance, max days), tax treatment and approval;
// logistics only reports the trip and keeps the claim's id and status on the task.
package perdiem

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	"github.com/bengobox/logistics-service/internal/modules/fleet"
	"github.com/bengobox/logistics-service/internal/platform/erp"
)

// MetaKey is the task metadata key holding the claim outcome shown in logistics-ui and the
// rider app: {status, claim_id, amount, days, code, message, updated_at}.
const MetaKey = "per_diem_claim"

// Claim outcomes recorded on the task.
const (
	StatusRaised  = "raised"  // claim exists in erp-api (pending, approved or paid there)
	StatusSkipped = "skipped" // erp-api declined it: not an employee, per diem off, trip too short
	StatusFailed  = "failed"  // erp-api unreachable; raise again from the task
)

// ErrNotStaff is returned when the rider is not a staff (payroll) rider.
var ErrNotStaff = errors.New("perdiem: rider is not a staff rider")

// Service raises claims through erp-api.
type Service struct {
	client *ent.Client
	erp    *erp.Client
	log    *zap.Logger
}

// NewService builds the per diem service.
func NewService(client *ent.Client, erpClient *erp.Client, log *zap.Logger) *Service {
	return &Service{client: client, erp: erpClient, log: log.Named("perdiem")}
}

// Enabled reports whether erp-api is configured.
func (s *Service) Enabled() bool { return s != nil && s.erp.Enabled() }

// Outcome is what happened to a claim request.
type Outcome struct {
	Status  string  `json:"status"`
	ClaimID string  `json:"claim_id,omitempty"`
	Amount  string  `json:"amount,omitempty"`
	Days    float64 `json:"days,omitempty"`
	Code    string  `json:"code,omitempty"`
	Message string  `json:"message,omitempty"`
}

// RaiseForTask asks erp-api for the per diem due on a completed task by a staff rider. days
// of 0 lets erp-api derive days from the trip window. Safe to call again: erp-api returns the
// claim already raised for the task.
func (s *Service) RaiseForTask(ctx context.Context, tenantID, taskID, memberID uuid.UUID, days float64) (*Outcome, error) {
	if !s.Enabled() {
		return nil, errors.New("perdiem: erp-api is not configured")
	}
	member, err := s.client.FleetMember.Get(ctx, memberID)
	if err != nil {
		return nil, fmt.Errorf("perdiem: load rider: %w", err)
	}
	if member.TenantID != tenantID || !fleet.EmploymentOf(member).IsStaff() {
		return nil, ErrNotStaff
	}
	user, err := s.client.User.Get(ctx, member.UserID)
	if err != nil {
		return nil, fmt.Errorf("perdiem: load rider user: %w", err)
	}
	authID := user.ID
	if user.AuthServiceUserID != uuid.Nil {
		authID = user.AuthServiceUserID
	}
	t, err := s.client.Task.Query().Where(task.ID(taskID), task.TenantID(tenantID)).Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("perdiem: load task: %w", err)
	}

	var started, ended *time.Time
	if a, aerr := s.client.TaskAssignment.Query().
		Where(taskassignment.TaskID(taskID), taskassignment.FleetMemberID(memberID)).
		Order(ent.Desc(taskassignment.FieldAssignedAt)).
		First(ctx); aerr == nil {
		started = a.AcceptedAt
		if started == nil {
			started = &a.AssignedAt
		}
		ended = a.CompletedAt
	}
	if ended == nil {
		now := time.Now()
		ended = &now
	}

	ref := map[string]any{"task_id": taskID.String(), "tracking_code": t.TrackingCode}
	for _, k := range []string{"zone_name", "dropoff_address", "order_number"} {
		if v, ok := t.Metadata[k].(string); ok && v != "" {
			ref[k] = v
		}
	}
	desc := "Per diem for delivery " + t.TrackingCode
	if z, ok := ref["zone_name"].(string); ok {
		desc += " to " + z
	}

	claim, err := s.erp.RaiseClaim(ctx, tenantID, erp.ClaimRequest{
		AuthUserID:  authID.String(),
		Source:      "logistics",
		SourceKey:   fmt.Sprintf("logistics:task:%s:per_diem", taskID),
		ClaimType:   "per_diem",
		Days:        days,
		DistanceKm:  metaNumber(t.Metadata["distance_km"]),
		StartedAt:   started,
		EndedAt:     ended,
		Description: desc,
		Reference:   ref,
	})

	out := &Outcome{Days: days}
	switch {
	case err == nil:
		out.Status, out.ClaimID, out.Amount = StatusRaised, claim.ID, claim.Amount
	default:
		if rej, ok := erp.IsRejection(err); ok {
			out.Status, out.Code, out.Message = StatusSkipped, rej.Code, rej.Message
		} else {
			out.Status, out.Message = StatusFailed, "HR payroll could not be reached; raise the claim again from the task"
			s.log.Warn("per diem claim failed", zap.String("task_id", taskID.String()), zap.Error(err))
		}
	}
	if serr := s.record(ctx, t, out); serr != nil {
		s.log.Warn("per diem outcome not saved on task", zap.Error(serr))
	}
	return out, nil
}

// record stores the outcome on the task metadata, keeping the other keys.
func (s *Service) record(ctx context.Context, t *ent.Task, out *Outcome) error {
	meta := map[string]any{}
	for k, v := range t.Metadata {
		meta[k] = v
	}
	entry := map[string]any{"status": out.Status, "updated_at": time.Now().UTC().Format(time.RFC3339)}
	if out.ClaimID != "" {
		entry["claim_id"] = out.ClaimID
	}
	if out.Amount != "" {
		entry["amount"] = out.Amount
	}
	if out.Days > 0 {
		entry["days"] = out.Days
	}
	if out.Code != "" {
		entry["code"] = out.Code
	}
	if out.Message != "" {
		entry["message"] = out.Message
	}
	meta[MetaKey] = entry
	return s.client.Task.UpdateOneID(t.ID).SetMetadata(meta).Exec(ctx)
}

func metaNumber(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}
