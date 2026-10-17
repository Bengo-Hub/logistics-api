package fleet

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	entuser "github.com/bengobox/logistics-service/internal/ent/user"
)

// OnboardingSourceCompany marks a rider the business created directly (fleet_members.onboarding_source).
const OnboardingSourceCompany = "company_created"

// OnboardRiderRequest is an internal rider the admin fills in completely instead of waiting
// for an invite. The rider is active at once: staff are vetted by HR, so the ID number is the
// one identity detail required and ID and photo uploads are optional. SendInvite also emails
// the rider so they can sign in; without it the rider links up on first sign-in with the same
// email.
type OnboardRiderRequest struct {
	Email     string `json:"email"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Phone     string `json:"phone"`
	IDNumber  string `json:"id_number"`
	LicenseNo string `json:"license_no,omitempty"`
	// PayBasis is payroll (default) or earning_rules; see PayBasis* in employment.go.
	PayBasis             string `json:"pay_basis,omitempty"`
	RiderPhoto           string `json:"rider_photo,omitempty"`
	IDPassportAttachment string `json:"id_passport_attachment,omitempty"`
	// VehicleID links an existing fleet vehicle; Vehicle registers a new one. At most one.
	VehicleID  *uuid.UUID            `json:"vehicle_id,omitempty"`
	Vehicle    *CreateVehicleRequest `json:"vehicle,omitempty"`
	SendInvite bool                  `json:"send_invite,omitempty"`
}

// validate trims the request in place and reports the first problem.
func (r *OnboardRiderRequest) validate() error {
	r.Email = strings.ToLower(strings.TrimSpace(r.Email))
	r.FirstName = strings.TrimSpace(r.FirstName)
	r.LastName = strings.TrimSpace(r.LastName)
	r.Phone = strings.TrimSpace(r.Phone)
	r.IDNumber = strings.TrimSpace(r.IDNumber)
	r.LicenseNo = strings.TrimSpace(r.LicenseNo)
	switch {
	case r.FirstName == "":
		return fmt.Errorf("fleet: first name is required")
	case !strings.Contains(r.Email, "@"):
		return fmt.Errorf("fleet: a valid email is required")
	case r.Phone == "":
		return fmt.Errorf("fleet: phone is required")
	case r.IDNumber == "":
		return fmt.Errorf("fleet: ID number is required")
	case r.VehicleID != nil && r.Vehicle != nil:
		return fmt.Errorf("fleet: link an existing vehicle or add a new one, not both")
	}
	return nil
}

// OnboardInternalRider creates an internal (staff) rider, active at once, with its vehicle, in
// one transaction. The stub user is linked to the rider's SSO account by email on first sign-in
// (identity.EnsureUserFromToken).
func (s *Service) OnboardInternalRider(ctx context.Context, tenantID uuid.UUID, tenantSlug string, req OnboardRiderRequest) (*ent.FleetMember, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	emp, err := Employment{Type: EmploymentStaff, PayBasis: req.PayBasis}.Normalize()
	if err != nil {
		return nil, err
	}
	fl, err := s.GetOrCreateFleet(ctx, tenantID, tenantSlug)
	if err != nil {
		return nil, err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: onboard: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	name := strings.TrimSpace(req.FirstName + " " + req.LastName)
	u, err := tx.User.Query().Where(entuser.TenantID(tenantID), entuser.EmailEqualFold(req.Email)).First(ctx)
	switch {
	case err == nil:
		taken, qerr := tx.FleetMember.Query().Where(fleetmember.TenantID(tenantID), fleetmember.UserID(u.ID)).Exist(ctx)
		if qerr != nil {
			return nil, fmt.Errorf("fleet: onboard: %w", qerr)
		}
		if taken {
			return nil, ErrAlreadyRider
		}
		upd := u.Update().SetFullName(name)
		if u.Phone == "" {
			upd.SetPhone(req.Phone)
		}
		if err := upd.Exec(ctx); err != nil {
			return nil, fmt.Errorf("fleet: onboard: update user: %w", err)
		}
	case ent.IsNotFound(err):
		u, err = tx.User.Create().
			SetTenantID(tenantID).
			SetEmail(req.Email).
			SetFullName(name).
			SetPhone(req.Phone).
			SetStatus("invited").
			SetSyncStatus("pending").
			Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("fleet: onboard: create user: %w", err)
		}
	default:
		return nil, fmt.Errorf("fleet: onboard: %w", err)
	}

	var vehicleID *uuid.UUID
	switch {
	case req.VehicleID != nil:
		if err := requireFreeVehicle(ctx, s.client, tx, tenantID, *req.VehicleID, uuid.Nil); err != nil {
			return nil, err
		}
		vehicleID = req.VehicleID
	case req.Vehicle != nil:
		v, err := CreateVehicleRecord(ctx, tx.Vehicle, tenantID, fl.ID, *req.Vehicle, nil)
		if err != nil {
			return nil, err
		}
		vehicleID = &v.ID
	}

	b := tx.FleetMember.Create().
		SetTenantID(tenantID).
		SetFleetID(fl.ID).
		SetUserID(u.ID).
		SetStatus("active").
		SetOnboardingSource(OnboardingSourceCompany).
		SetIDNumber(req.IDNumber).
		SetMetadata(map[string]any{MetaEmployment: emp.ToMap(), "source": "admin_onboarded"})
	if req.LicenseNo != "" {
		b.SetLicenseNo(req.LicenseNo)
	}
	if req.RiderPhoto != "" {
		b.SetRiderPhoto(req.RiderPhoto)
	}
	if req.IDPassportAttachment != "" {
		b.SetIDPassportAttachment(req.IDPassportAttachment)
	}
	if vehicleID != nil {
		b.SetVehicleID(*vehicleID)
	}
	m, err := b.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrAlreadyRider
		}
		return nil, fmt.Errorf("fleet: onboard: create rider: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("fleet: onboard: %w", err)
	}

	s.ensureDriverRole(ctx, tenantID, u.ID)
	s.statusChanged(ctx, tenantID)
	s.log.Info("internal rider onboarded",
		zap.String("member_id", m.ID.String()),
		zap.String("pay_basis", emp.PayBasis),
		zap.Bool("invite", req.SendInvite),
	)
	if req.SendInvite {
		s.publishInvited(ctx, tenantID, tenantSlug, m)
	}
	return s.GetMember(ctx, tenantID, m.ID)
}
