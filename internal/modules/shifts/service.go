// Package shifts owns rider shifts: dispatchers schedule them, riders go online and offline
// against them, and auto-dispatch prefers riders whose active shift covers the drop-off area.
// Every read and write is scoped to the tenant; the old handler looked shifts up by id alone.
package shifts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/ridershift"
)

// Shift statuses.
const (
	StatusScheduled = "scheduled"
	StatusActive    = "active"
	StatusCompleted = "completed"
	StatusCancelled = "cancelled"
)

// openEndedShift is how long a shift started by a rider going online (with nothing scheduled)
// lasts if they never go offline. Going offline ends it at that moment.
const openEndedShift = 12 * time.Hour

// earlyStart is how early a rider may go online and start their next scheduled shift.
const earlyStart = 30 * time.Minute

var (
	ErrNotFound      = errors.New("shifts: shift not found")
	ErrInvalid       = errors.New("shifts: invalid shift")
	ErrTransition    = errors.New("shifts: shift cannot move to that status")
	ErrOverlap       = errors.New("shifts: rider already has a shift in that time")
	ErrRiderNotFound = errors.New("shifts: rider not found in this business")
	ErrRiderInactive = errors.New("shifts: rider is not active")
)

// Shift is the API view: the stored shift plus the rider's name.
type Shift struct {
	ID            uuid.UUID   `json:"id"`
	FleetMemberID uuid.UUID   `json:"fleet_member_id"`
	RiderName     string      `json:"rider_name"`
	RiderPhone    string      `json:"rider_phone,omitempty"`
	ShiftStart    time.Time   `json:"shift_start"`
	ShiftEnd      time.Time   `json:"shift_end"`
	Status        string      `json:"status"`
	ZoneIDs       []uuid.UUID `json:"zone_ids"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// ListFilter narrows List. Zero values mean "any".
type ListFilter struct {
	Status   string
	MemberID uuid.UUID
	From     *time.Time // shifts ending after From
	To       *time.Time // shifts starting before To
	Limit    int
	Offset   int
}

// CreateRequest schedules a shift.
type CreateRequest struct {
	FleetMemberID uuid.UUID
	Start, End    time.Time
	ZoneIDs       []uuid.UUID
}

// Service manages shifts.
type Service struct {
	client *ent.Client
	now    func() time.Time
}

// NewService creates the shift service.
func NewService(client *ent.Client) *Service {
	return &Service{client: client, now: time.Now}
}

// List returns the tenant's shifts, newest start first, with the total for paging.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID, f ListFilter) ([]Shift, int, error) {
	q := s.client.RiderShift.Query().Where(ridershift.TenantID(tenantID))
	if f.Status != "" {
		if !validStatus(f.Status) {
			return nil, 0, fmt.Errorf("%w: unknown status %q", ErrInvalid, f.Status)
		}
		q = q.Where(ridershift.StatusEQ(ridershift.Status(f.Status)))
	}
	if f.MemberID != uuid.Nil {
		q = q.Where(ridershift.FleetMemberID(f.MemberID))
	}
	if f.From != nil {
		q = q.Where(ridershift.ShiftEndGTE(*f.From))
	}
	if f.To != nil {
		q = q.Where(ridershift.ShiftStartLTE(*f.To))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("shifts: count: %w", err)
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := q.Order(ent.Desc(ridershift.FieldShiftStart)).Limit(limit).Offset(f.Offset).All(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("shifts: list: %w", err)
	}
	out, err := s.withRiders(ctx, tenantID, rows)
	return out, total, err
}

// Get returns one of the tenant's shifts.
func (s *Service) Get(ctx context.Context, tenantID, id uuid.UUID) (*Shift, error) {
	row, err := s.load(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	return s.one(ctx, tenantID, row)
}

// Create schedules a shift for an active rider of the tenant. Shifts of one rider may not overlap.
func (s *Service) Create(ctx context.Context, tenantID uuid.UUID, req CreateRequest) (*Shift, error) {
	if !req.End.After(req.Start) {
		return nil, fmt.Errorf("%w: the shift must end after it starts", ErrInvalid)
	}
	if req.End.Sub(req.Start) > 24*time.Hour {
		return nil, fmt.Errorf("%w: a shift can be at most 24 hours", ErrInvalid)
	}
	if err := s.requireActiveRider(ctx, tenantID, req.FleetMemberID); err != nil {
		return nil, err
	}
	if err := s.checkOverlap(ctx, tenantID, req.FleetMemberID, req.Start, req.End, uuid.Nil); err != nil {
		return nil, err
	}
	b := s.client.RiderShift.Create().
		SetTenantID(tenantID).
		SetFleetMemberID(req.FleetMemberID).
		SetShiftStart(req.Start).
		SetShiftEnd(req.End)
	if len(req.ZoneIDs) > 0 {
		b = b.SetZoneIds(req.ZoneIDs)
	}
	row, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("shifts: create: %w", err)
	}
	return s.one(ctx, tenantID, row)
}

// Start makes a scheduled shift active.
func (s *Service) Start(ctx context.Context, tenantID, id uuid.UUID) (*Shift, error) {
	return s.transition(ctx, tenantID, id, StatusActive)
}

// End completes an active shift. Ending early records the actual end time.
func (s *Service) End(ctx context.Context, tenantID, id uuid.UUID) (*Shift, error) {
	return s.transition(ctx, tenantID, id, StatusCompleted)
}

// Cancel cancels a scheduled shift.
func (s *Service) Cancel(ctx context.Context, tenantID, id uuid.UUID) (*Shift, error) {
	return s.transition(ctx, tenantID, id, StatusCancelled)
}

// SetStatus applies a status change by name (the PATCH route).
func (s *Service) SetStatus(ctx context.Context, tenantID, id uuid.UUID, status string) (*Shift, error) {
	if !validStatus(status) {
		return nil, fmt.Errorf("%w: unknown status %q", ErrInvalid, status)
	}
	return s.transition(ctx, tenantID, id, status)
}

// Delete removes a shift that never ran (scheduled or cancelled). Worked shifts are history.
func (s *Service) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	row, err := s.load(ctx, tenantID, id)
	if err != nil {
		return err
	}
	if row.Status != ridershift.StatusScheduled && row.Status != ridershift.StatusCancelled {
		return fmt.Errorf("%w: only a shift that has not run can be deleted", ErrTransition)
	}
	return s.client.RiderShift.DeleteOneID(row.ID).Exec(ctx)
}

// Current returns the rider's active shift (nil when offline) and their next scheduled one.
func (s *Service) Current(ctx context.Context, tenantID, memberID uuid.UUID) (active, next *Shift, err error) {
	now := s.now()
	a, err := s.client.RiderShift.Query().
		Where(ridershift.TenantID(tenantID), ridershift.FleetMemberID(memberID), ridershift.StatusEQ(ridershift.StatusActive)).
		Order(ent.Desc(ridershift.FieldShiftStart)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, nil, fmt.Errorf("shifts: current: %w", err)
	}
	n, err := s.client.RiderShift.Query().
		Where(
			ridershift.TenantID(tenantID), ridershift.FleetMemberID(memberID),
			ridershift.StatusEQ(ridershift.StatusScheduled), ridershift.ShiftEndGT(now),
		).
		Order(ent.Asc(ridershift.FieldShiftStart)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, nil, fmt.Errorf("shifts: next: %w", err)
	}
	if a != nil {
		if active, err = s.one(ctx, tenantID, a); err != nil {
			return nil, nil, err
		}
	}
	if n != nil {
		if next, err = s.one(ctx, tenantID, n); err != nil {
			return nil, nil, err
		}
	}
	return active, next, nil
}

// GoOnline puts the rider on shift: their active shift if one is running, else their scheduled
// shift starting within the next 30 minutes (or already under way), else an open-ended shift
// from now. The rider must be active in the fleet.
func (s *Service) GoOnline(ctx context.Context, tenantID, memberID uuid.UUID) (*Shift, error) {
	if err := s.requireActiveRider(ctx, tenantID, memberID); err != nil {
		return nil, err
	}
	now := s.now()
	active, err := s.client.RiderShift.Query().
		Where(ridershift.TenantID(tenantID), ridershift.FleetMemberID(memberID), ridershift.StatusEQ(ridershift.StatusActive)).
		First(ctx)
	if err == nil {
		return s.one(ctx, tenantID, active)
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("shifts: online: %w", err)
	}
	sched, err := s.client.RiderShift.Query().
		Where(
			ridershift.TenantID(tenantID), ridershift.FleetMemberID(memberID),
			ridershift.StatusEQ(ridershift.StatusScheduled),
			ridershift.ShiftStartLTE(now.Add(earlyStart)), ridershift.ShiftEndGT(now),
		).
		Order(ent.Asc(ridershift.FieldShiftStart)).
		First(ctx)
	if err == nil {
		row, uerr := sched.Update().SetStatus(ridershift.StatusActive).Save(ctx)
		if uerr != nil {
			return nil, fmt.Errorf("shifts: start scheduled: %w", uerr)
		}
		return s.one(ctx, tenantID, row)
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("shifts: online: %w", err)
	}
	// Nothing scheduled: an open-ended shift, ended when the rider goes offline. It stops short
	// of the rider's next scheduled shift so the two never overlap.
	end := now.Add(openEndedShift)
	if next, nerr := s.client.RiderShift.Query().
		Where(
			ridershift.TenantID(tenantID), ridershift.FleetMemberID(memberID),
			ridershift.StatusEQ(ridershift.StatusScheduled), ridershift.ShiftStartGT(now),
		).
		Order(ent.Asc(ridershift.FieldShiftStart)).
		First(ctx); nerr == nil && next.ShiftStart.Before(end) {
		end = next.ShiftStart
	}
	row, err := s.client.RiderShift.Create().
		SetTenantID(tenantID).
		SetFleetMemberID(memberID).
		SetShiftStart(now).
		SetShiftEnd(end).
		SetStatus(ridershift.StatusActive).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("shifts: online: %w", err)
	}
	return s.one(ctx, tenantID, row)
}

// GoOffline ends the rider's active shift now. It returns the ended shift, or nil when the
// rider was not on shift.
func (s *Service) GoOffline(ctx context.Context, tenantID, memberID uuid.UUID) (*Shift, error) {
	rows, err := s.client.RiderShift.Query().
		Where(ridershift.TenantID(tenantID), ridershift.FleetMemberID(memberID), ridershift.StatusEQ(ridershift.StatusActive)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("shifts: offline: %w", err)
	}
	var last *ent.RiderShift
	for _, row := range rows {
		ended, err := s.complete(ctx, row)
		if err != nil {
			return nil, err
		}
		last = ended
	}
	if last == nil {
		return nil, nil
	}
	return s.one(ctx, tenantID, last)
}

// ─── internals ──────────────────────────────────────────────────────────────

func validStatus(s string) bool {
	switch s {
	case StatusScheduled, StatusActive, StatusCompleted, StatusCancelled:
		return true
	}
	return false
}

// allowed is the shift state machine.
func allowed(from, to string) bool {
	switch from {
	case StatusScheduled:
		return to == StatusActive || to == StatusCancelled
	case StatusActive:
		return to == StatusCompleted
	}
	return false
}

func (s *Service) load(ctx context.Context, tenantID, id uuid.UUID) (*ent.RiderShift, error) {
	row, err := s.client.RiderShift.Query().
		Where(ridershift.ID(id), ridershift.TenantID(tenantID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("shifts: get: %w", err)
	}
	return row, nil
}

func (s *Service) transition(ctx context.Context, tenantID, id uuid.UUID, to string) (*Shift, error) {
	row, err := s.load(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if string(row.Status) == to {
		return s.one(ctx, tenantID, row)
	}
	if !allowed(string(row.Status), to) {
		return nil, fmt.Errorf("%w: %s to %s", ErrTransition, row.Status, to)
	}
	if to == StatusCompleted {
		row, err = s.complete(ctx, row)
	} else {
		row, err = row.Update().SetStatus(ridershift.Status(to)).Save(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("shifts: update: %w", err)
	}
	return s.one(ctx, tenantID, row)
}

// complete ends an active shift, recording the actual end when it ends early.
func (s *Service) complete(ctx context.Context, row *ent.RiderShift) (*ent.RiderShift, error) {
	upd := row.Update().SetStatus(ridershift.StatusCompleted)
	if now := s.now(); now.Before(row.ShiftEnd) {
		end := now
		if end.Before(row.ShiftStart) {
			end = row.ShiftStart
		}
		upd = upd.SetShiftEnd(end)
	}
	out, err := upd.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("shifts: end: %w", err)
	}
	return out, nil
}

func (s *Service) requireActiveRider(ctx context.Context, tenantID, memberID uuid.UUID) error {
	m, err := s.client.FleetMember.Query().
		Where(fleetmember.ID(memberID), fleetmember.TenantID(tenantID)).
		Only(ctx)
	if ent.IsNotFound(err) {
		return ErrRiderNotFound
	}
	if err != nil {
		return fmt.Errorf("shifts: rider: %w", err)
	}
	if m.Status != "active" {
		return ErrRiderInactive
	}
	return nil
}

func (s *Service) checkOverlap(ctx context.Context, tenantID, memberID uuid.UUID, start, end time.Time, except uuid.UUID) error {
	q := s.client.RiderShift.Query().Where(
		ridershift.TenantID(tenantID),
		ridershift.FleetMemberID(memberID),
		ridershift.StatusIn(ridershift.StatusScheduled, ridershift.StatusActive),
		ridershift.ShiftStartLT(end),
		ridershift.ShiftEndGT(start),
	)
	if except != uuid.Nil {
		q = q.Where(ridershift.IDNEQ(except))
	}
	n, err := q.Count(ctx)
	if err != nil {
		return fmt.Errorf("shifts: overlap: %w", err)
	}
	if n > 0 {
		return ErrOverlap
	}
	return nil
}

func (s *Service) one(ctx context.Context, tenantID uuid.UUID, row *ent.RiderShift) (*Shift, error) {
	out, err := s.withRiders(ctx, tenantID, []*ent.RiderShift{row})
	if err != nil {
		return nil, err
	}
	return &out[0], nil
}

// withRiders adds rider names with one query for the whole page.
func (s *Service) withRiders(ctx context.Context, tenantID uuid.UUID, rows []*ent.RiderShift) ([]Shift, error) {
	ids := make([]uuid.UUID, 0, len(rows))
	seen := map[uuid.UUID]bool{}
	for _, r := range rows {
		if !seen[r.FleetMemberID] {
			seen[r.FleetMemberID] = true
			ids = append(ids, r.FleetMemberID)
		}
	}
	names := map[uuid.UUID][2]string{}
	if len(ids) > 0 {
		members, err := s.client.FleetMember.Query().
			Where(fleetmember.TenantID(tenantID), fleetmember.IDIn(ids...)).
			WithUser().
			All(ctx)
		if err != nil {
			return nil, fmt.Errorf("shifts: riders: %w", err)
		}
		for _, m := range members {
			if u := m.Edges.User; u != nil {
				name := u.FullName
				if name == "" {
					name = u.Email
				}
				names[m.ID] = [2]string{name, u.Phone}
			}
		}
	}
	out := make([]Shift, 0, len(rows))
	for _, r := range rows {
		n := names[r.FleetMemberID]
		zones := r.ZoneIds
		if zones == nil {
			zones = []uuid.UUID{}
		}
		out = append(out, Shift{
			ID: r.ID, FleetMemberID: r.FleetMemberID, RiderName: n[0], RiderPhone: n[1],
			ShiftStart: r.ShiftStart, ShiftEnd: r.ShiftEnd, Status: string(r.Status), ZoneIDs: zones,
			CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		})
	}
	return out, nil
}
