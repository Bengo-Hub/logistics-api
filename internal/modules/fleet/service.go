package fleet

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	entfleet "github.com/bengobox/logistics-service/internal/ent/fleet"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	entuser "github.com/bengobox/logistics-service/internal/ent/user"
	"github.com/bengobox/logistics-service/internal/platform/events"
)

// InviteMemberRequest is the DTO for adding a rider to a fleet.
type InviteMemberRequest struct {
	UserID   uuid.UUID `json:"user_id"`
	FleetID  uuid.UUID `json:"fleet_id,omitempty"`
	IDNumber string    `json:"id_number,omitempty"`
	LicenseNo string   `json:"license_no,omitempty"`
}

// Service handles fleet and fleet member business logic.
type Service struct {
	client    *ent.Client
	log       *zap.Logger
	publisher *events.Publisher
}

// NewService creates a new fleet service.
func NewService(client *ent.Client, log *zap.Logger, publisher *events.Publisher) *Service {
	return &Service{
		client:    client,
		log:       log.Named("fleet.service"),
		publisher: publisher,
	}
}

// resolveUserInfo looks up user email and name from the local User table.
func (s *Service) resolveUserInfo(ctx context.Context, userID uuid.UUID) (email, name string) {
	u, err := s.client.User.Get(ctx, userID)
	if err != nil {
		s.log.Warn("could not resolve user for event", zap.String("user_id", userID.String()), zap.Error(err))
		return "", ""
	}
	return u.Email, u.FullName
}

// GetOrCreateFleet returns the tenant's active fleet, creating one if none exists.
func (s *Service) GetOrCreateFleet(ctx context.Context, tenantID uuid.UUID, tenantSlug string) (*ent.Fleet, error) {
	fl, err := s.client.Fleet.Query().
		Where(entfleet.TenantID(tenantID), entfleet.Status("active")).
		WithMembers(func(q *ent.FleetMemberQuery) {
			q.Limit(100)
		}).
		First(ctx)
	if err == nil {
		return fl, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("fleet: query: %w", err)
	}

	slug := tenantSlug
	if slug == "" {
		slug = tenantID.String()
	}

	fl, err = s.client.Fleet.Create().
		SetTenantID(tenantID).
		SetTenantSlug(slug).
		SetName("Default Fleet").
		SetType("internal").
		SetStatus("active").
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: create default: %w", err)
	}
	s.log.Info("created default fleet", zap.String("fleet_id", fl.ID.String()))
	return fl, nil
}

// ListMembers returns fleet members for a tenant.
func (s *Service) ListMembers(ctx context.Context, tenantID uuid.UUID, status string) ([]*ent.FleetMember, error) {
	q := s.client.FleetMember.Query().
		Where(fleetmember.TenantID(tenantID)).
		WithVehicle().
		WithUser()

	if status != "" {
		q = q.Where(fleetmember.Status(status))
	}

	members, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: list members: %w", err)
	}
	return members, nil
}

// GetMember returns a specific fleet member.
func (s *Service) GetMember(ctx context.Context, tenantID, memberID uuid.UUID) (*ent.FleetMember, error) {
	m, err := s.client.FleetMember.Query().
		Where(fleetmember.ID(memberID), fleetmember.TenantID(tenantID)).
		WithVehicle().
		WithUser().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("fleet: member not found")
		}
		return nil, fmt.Errorf("fleet: get member: %w", err)
	}
	return m, nil
}

// InviteByEmailRequest is the simplified DTO for inviting a rider by email.
type InviteByEmailRequest struct {
	Email    string `json:"email"`
	IDNumber string `json:"id_number,omitempty"`
}

// InviteMemberByEmail invites a rider using just email and ID number.
// Creates a stub User if one doesn't exist for the tenant.
func (s *Service) InviteMemberByEmail(ctx context.Context, tenantID uuid.UUID, tenantSlug string, req InviteByEmailRequest) (*ent.FleetMember, error) {
	// Look up existing user by email + tenant
	existing, err := s.client.User.Query().
		Where(entuser.TenantID(tenantID), entuser.Email(req.Email)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("fleet: query user by email: %w", err)
	}

	var userID uuid.UUID
	if existing != nil {
		userID = existing.ID
	} else {
		// Create a stub user with "invited" status
		stub, createErr := s.client.User.Create().
			SetTenantID(tenantID).
			SetEmail(req.Email).
			SetFullName(req.Email). // Placeholder until SSO registration
			SetStatus("invited").
			SetSyncStatus("pending").
			Save(ctx)
		if createErr != nil {
			return nil, fmt.Errorf("fleet: create stub user: %w", createErr)
		}
		userID = stub.ID
		s.log.Info("stub user created for rider invite",
			zap.String("email", req.Email),
			zap.String("user_id", userID.String()),
		)
	}

	return s.InviteMember(ctx, tenantID, tenantSlug, InviteMemberRequest{
		UserID:   userID,
		IDNumber: req.IDNumber,
	})
}

// InviteMember adds a rider to the fleet in "pending" status.
func (s *Service) InviteMember(ctx context.Context, tenantID uuid.UUID, tenantSlug string, req InviteMemberRequest) (*ent.FleetMember, error) {
	// Check for duplicate membership
	existing, _ := s.client.FleetMember.Query().
		Where(fleetmember.TenantID(tenantID), fleetmember.UserID(req.UserID)).
		First(ctx)
	if existing != nil {
		return existing, nil
	}

	fleetID := req.FleetID
	if fleetID == uuid.Nil {
		fl, err := s.GetOrCreateFleet(ctx, tenantID, tenantSlug)
		if err != nil {
			return nil, err
		}
		fleetID = fl.ID
	}

	builder := s.client.FleetMember.Create().
		SetTenantID(tenantID).
		SetFleetID(fleetID).
		SetUserID(req.UserID).
		SetStatus("pending")

	if req.IDNumber != "" {
		builder.SetIDNumber(req.IDNumber)
	}
	if req.LicenseNo != "" {
		builder.SetLicenseNo(req.LicenseNo)
	}

	m, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: invite member: %w", err)
	}

	s.log.Info("fleet member invited",
		zap.String("member_id", m.ID.String()),
		zap.String("user_id", req.UserID.String()),
	)

	// Publish fleet member invited event
	if s.publisher != nil {
		email, name := s.resolveUserInfo(ctx, req.UserID)
		if email != "" {
			if pubErr := s.publisher.PublishFleetMemberInvited(ctx, tenantID, events.FleetMemberEventData{
				MemberID: m.ID.String(), UserID: req.UserID.String(),
				FleetID: fleetID.String(), UserEmail: email, UserName: name,
			}); pubErr != nil {
				s.log.Warn("failed to publish fleet.member_invited", zap.Error(pubErr))
			}
		}
	}

	return m, nil
}

// ApproveMember transitions a member from pending → approved → active.
func (s *Service) ApproveMember(ctx context.Context, tenantID, memberID uuid.UUID) (*ent.FleetMember, error) {
	m, err := s.client.FleetMember.Query().
		Where(fleetmember.ID(memberID), fleetmember.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("fleet: member not found")
		}
		return nil, fmt.Errorf("fleet: get for approve: %w", err)
	}
	updated, err := s.client.FleetMember.UpdateOne(m).SetStatus("active").Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: approve: %w", err)
	}

	// Publish fleet member approved event
	if s.publisher != nil {
		email, name := s.resolveUserInfo(ctx, m.UserID)
		if email != "" {
			if pubErr := s.publisher.PublishFleetMemberApproved(ctx, tenantID, events.FleetMemberEventData{
				MemberID: memberID.String(), UserID: m.UserID.String(),
				FleetID: m.FleetID.String(), UserEmail: email, UserName: name,
			}); pubErr != nil {
				s.log.Warn("failed to publish fleet.member_approved", zap.Error(pubErr))
			}
		}
	}

	return updated, nil
}

// SuspendMember transitions a member to suspended status.
func (s *Service) SuspendMember(ctx context.Context, tenantID, memberID uuid.UUID) (*ent.FleetMember, error) {
	m, err := s.client.FleetMember.Query().
		Where(fleetmember.ID(memberID), fleetmember.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("fleet: member not found")
		}
		return nil, fmt.Errorf("fleet: get for suspend: %w", err)
	}
	updated, err := s.client.FleetMember.UpdateOne(m).SetStatus("suspended").Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: suspend: %w", err)
	}

	// Publish fleet member suspended event
	if s.publisher != nil {
		email, name := s.resolveUserInfo(ctx, m.UserID)
		if email != "" {
			if pubErr := s.publisher.PublishFleetMemberSuspended(ctx, tenantID, events.FleetMemberEventData{
				MemberID: memberID.String(), UserID: m.UserID.String(),
				FleetID: m.FleetID.String(), UserEmail: email, UserName: name,
			}); pubErr != nil {
				s.log.Warn("failed to publish fleet.member_suspended", zap.Error(pubErr))
			}
		}
	}

	return updated, nil
}

// RateRiderRequest is the DTO for rating a rider after delivery.
type RateRiderRequest struct {
	TaskID         *uuid.UUID `json:"task_id,omitempty"`
	OrderID        string     `json:"order_id,omitempty"`
	CustomerUserID string     `json:"customer_user_id,omitempty"`
	Rating         int        `json:"rating"`
	Comment        string     `json:"comment,omitempty"`
}

// RateRider records a customer rating for a rider and updates the running average.
func (s *Service) RateRider(ctx context.Context, tenantID, memberID uuid.UUID, req RateRiderRequest) (*ent.RiderRating, error) {
	if req.Rating < 1 || req.Rating > 5 {
		return nil, fmt.Errorf("fleet: rating must be between 1 and 5")
	}

	// Verify member exists and belongs to tenant
	member, err := s.client.FleetMember.Query().
		Where(fleetmember.ID(memberID), fleetmember.TenantID(tenantID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("fleet: member not found")
		}
		return nil, fmt.Errorf("fleet: get member for rating: %w", err)
	}

	// Create the rating
	builder := s.client.RiderRating.Create().
		SetTenantID(tenantID).
		SetFleetMemberID(memberID).
		SetRating(req.Rating)

	if req.TaskID != nil {
		builder.SetTaskID(*req.TaskID)
	}
	if req.OrderID != "" {
		builder.SetOrderID(req.OrderID)
	}
	if req.CustomerUserID != "" {
		builder.SetCustomerUserID(req.CustomerUserID)
	}
	if req.Comment != "" {
		builder.SetComment(req.Comment)
	}

	rating, err := builder.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: create rating: %w", err)
	}

	// Update running average on fleet member
	oldAvg := member.AverageRating
	oldCount := member.TotalRatings
	newCount := oldCount + 1
	newAvg := ((oldAvg * float64(oldCount)) + float64(req.Rating)) / float64(newCount)

	_, err = s.client.FleetMember.UpdateOne(member).
		SetAverageRating(newAvg).
		SetTotalRatings(newCount).
		Save(ctx)
	if err != nil {
		s.log.Error("failed to update rider average rating", zap.Error(err))
	}

	s.log.Info("rider rated",
		zap.String("member_id", memberID.String()),
		zap.Int("rating", req.Rating),
		zap.Float64("new_average", newAvg),
		zap.Int("total_ratings", newCount),
	)

	return rating, nil
}
