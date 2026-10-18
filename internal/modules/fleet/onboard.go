package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	entuser "github.com/bengobox/logistics-service/internal/ent/user"
	entvehicle "github.com/bengobox/logistics-service/internal/ent/vehicle"
	"github.com/bengobox/logistics-service/internal/platform/authapi"
	"github.com/bengobox/logistics-service/internal/platform/notify"
)

// OnboardingSourceCompany marks a rider the business created directly (fleet_members.onboarding_source).
const OnboardingSourceCompany = "company_created"

// riderAccountTemplate is the notifications-api email an onboarded internal rider receives.
const riderAccountTemplate = "logistics/rider_account"

// Errors for the account step, which the handler maps to 502 and 409.
var (
	ErrAccountsUnavailable = errors.New("fleet: the accounts service could not create the rider's sign-in")
	ErrOtherBusiness       = errors.New("fleet: this email already rides for another business on the platform")
)

// SetAccounts wires auth-api (rider SSO accounts), notifications-api (the rider account email)
// and the rider-app address the email links to.
func (s *Service) SetAccounts(auth *authapi.Client, mail *notify.Client, riderAppURL string) {
	s.auth, s.mail, s.riderAppURL = auth, mail, strings.TrimRight(riderAppURL, "/")
}

// OnboardRiderRequest is an internal rider the admin fills in completely instead of waiting
// for an invite. The rider is active at once: staff are vetted by HR, so the ID number is the
// one identity detail required and ID and photo uploads are optional.
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
	VehicleID *uuid.UUID            `json:"vehicle_id,omitempty"`
	Vehicle   *CreateVehicleRequest `json:"vehicle,omitempty"`
	// SendInvite emails the rider their sign-in details (with the temporary password when the
	// account is new). Without it the admin hands the details over.
	SendInvite bool `json:"send_invite,omitempty"`
}

// OnboardResult is the new rider plus what the admin must see once.
type OnboardResult struct {
	Member *ent.FleetMember
	// TempPassword is set only when auth-api created the account in this call. Shown to the admin
	// once; never stored or logged.
	TempPassword string
	// ExistingAccount means the email already had a platform account: the rider signs in with
	// their own password.
	ExistingAccount bool
	EmailSent       bool
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

func (r OnboardRiderRequest) fullName() string {
	return strings.TrimSpace(r.FirstName + " " + r.LastName)
}

// OnboardInternalRider creates an internal (staff) rider, active at once, with an SSO account
// and its vehicle.
//
// Order matters. Everything that can refuse the request is checked first, so a refusal never
// leaves an auth account behind. Then auth-api creates (or finds) the account, which sets a
// temporary password the rider must change on first sign-in. Then one transaction writes the
// local user under the auth id (the key RBAC uses), the vehicle and the member. The email goes
// last, so it never announces a rider who was not saved.
func (s *Service) OnboardInternalRider(ctx context.Context, tenantID uuid.UUID, tenantSlug string, req OnboardRiderRequest) (*OnboardResult, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	emp, err := Employment{Type: EmploymentStaff, PayBasis: req.PayBasis}.Normalize()
	if err != nil {
		return nil, err
	}
	if err := s.precheckOnboard(ctx, tenantID, req); err != nil {
		return nil, err
	}
	fl, err := s.GetOrCreateFleet(ctx, tenantID, tenantSlug)
	if err != nil {
		return nil, err
	}

	account, err := s.auth.AddMember(ctx, tenantID, authapi.MemberRequest{
		Email:       req.Email,
		Name:        req.fullName(),
		Phone:       req.Phone,
		Roles:       []string{authapi.RoleDriver},
		MergeRoles:  true,
		SkipWelcome: true,
	})
	if err != nil {
		s.log.Error("onboard rider: auth account", zap.Error(err))
		return nil, fmt.Errorf("%w: %v", ErrAccountsUnavailable, err)
	}

	// auth.user.created can land between our read and our insert and create the user first; the
	// second attempt then finds it.
	var m *ent.FleetMember
	for attempt := 0; ; attempt++ {
		m, err = s.saveOnboarded(ctx, tenantID, fl.ID, account.UserID, emp, req)
		if err == nil || attempt == 1 || !ent.IsConstraintError(err) || errors.Is(err, ErrPlateInUse) {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	s.ensureDriverRole(ctx, tenantID, account.UserID)
	s.statusChanged(ctx, tenantID)
	res := &OnboardResult{TempPassword: account.TempPassword, ExistingAccount: account.TempPassword == ""}
	if req.SendInvite {
		res.EmailSent = s.sendRiderAccountEmail(ctx, tenantID, tenantSlug, m.ID, req, account.TempPassword)
	}
	s.log.Info("internal rider onboarded",
		zap.String("member_id", m.ID.String()),
		zap.String("pay_basis", emp.PayBasis),
		zap.Bool("new_account", !res.ExistingAccount),
		zap.Bool("email_sent", res.EmailSent),
	)
	if res.Member, err = s.GetMember(ctx, tenantID, m.ID); err != nil {
		return nil, err
	}
	return res, nil
}

// precheckOnboard refuses what would fail later: the person is already a rider here, the
// chosen vehicle is taken or foreign, or the new plate exists.
func (s *Service) precheckOnboard(ctx context.Context, tenantID uuid.UUID, req OnboardRiderRequest) error {
	rider, err := s.client.FleetMember.Query().
		Where(fleetmember.TenantID(tenantID), fleetmember.HasUserWith(entuser.EmailEqualFold(req.Email))).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("fleet: onboard: %w", err)
	}
	if rider {
		return ErrAlreadyRider
	}
	if req.VehicleID != nil {
		if err := requireFreeVehicle(ctx, s.client, nil, tenantID, *req.VehicleID, uuid.Nil); err != nil {
			return err
		}
	}
	if req.Vehicle != nil {
		if _, err := NormalizeVehicleType(req.Vehicle.VehicleType); err != nil {
			return err
		}
		// A bad policy must be refused before the auth account is created.
		if req.Vehicle.Insurance != nil {
			if _, err := req.Vehicle.Insurance.validate(time.Now(), ""); err != nil {
				return err
			}
		}
		if req.Vehicle.MemberID != nil {
			return fmt.Errorf("fleet: a new rider's vehicle is linked to them automatically")
		}
		plate := NormalizePlate(req.Vehicle.LicensePlate)
		if plate == "" {
			return fmt.Errorf("fleet: number plate is required")
		}
		taken, err := s.client.Vehicle.Query().
			Where(entvehicle.TenantID(tenantID), entvehicle.LicensePlate(plate)).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("fleet: onboard: %w", err)
		}
		if taken {
			return ErrPlateInUse
		}
	}
	return nil
}

// saveOnboarded writes the local user (keyed on the auth id), the vehicle and the active member
// in one transaction.
func (s *Service) saveOnboarded(ctx context.Context, tenantID, fleetID, authID uuid.UUID, emp Employment, req OnboardRiderRequest) (*ent.FleetMember, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: onboard: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := s.resolveOnboardUser(ctx, tx, tenantID, authID, req); err != nil {
		return nil, err
	}
	taken, err := tx.FleetMember.Query().Where(fleetmember.TenantID(tenantID), fleetmember.UserID(authID)).Exist(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: onboard: %w", err)
	}
	if taken {
		return nil, ErrAlreadyRider
	}

	var vehicleID *uuid.UUID
	switch {
	case req.VehicleID != nil:
		if err := requireFreeVehicle(ctx, s.client, tx, tenantID, *req.VehicleID, uuid.Nil); err != nil {
			return nil, err
		}
		vehicleID = req.VehicleID
	case req.Vehicle != nil:
		v, err := createVehicleInTx(ctx, tx, tenantID, fleetID, *req.Vehicle, nil, "admin")
		if err != nil {
			return nil, err
		}
		vehicleID = &v.ID
	}

	b := tx.FleetMember.Create().
		SetTenantID(tenantID).
		SetFleetID(fleetID).
		SetUserID(authID).
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
		return nil, fmt.Errorf("fleet: onboard: create rider: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("fleet: onboard: %w", err)
	}
	return m, nil
}

// resolveOnboardUser returns the local user under the auth id: the existing one, an older stub
// with this email moved onto the auth id, or a new row.
func (s *Service) resolveOnboardUser(ctx context.Context, tx *ent.Tx, tenantID, authID uuid.UUID, req OnboardRiderRequest) (*ent.User, error) {
	u, err := tx.User.Get(ctx, authID)
	switch {
	case err == nil:
		// User ids are platform-wide here, so one account rides for one business.
		if u.TenantID != tenantID {
			return nil, ErrOtherBusiness
		}
		upd := u.Update().SetFullName(req.fullName())
		if u.Phone == "" {
			upd.SetPhone(req.Phone)
		}
		return upd.Save(ctx)
	case !ent.IsNotFound(err):
		return nil, fmt.Errorf("fleet: onboard: %w", err)
	}

	stub, err := tx.User.Query().Where(entuser.TenantID(tenantID), entuser.EmailEqualFold(req.Email)).First(ctx)
	if err == nil {
		return RekeyUser(ctx, tx, stub, authID, RekeyFields{FullName: req.fullName(), Phone: req.Phone, Role: authapi.RoleDriver})
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("fleet: onboard: %w", err)
	}
	return tx.User.Create().
		SetID(authID).
		SetAuthServiceUserID(authID).
		SetTenantID(tenantID).
		SetEmail(req.Email).
		SetFullName(req.fullName()).
		SetPhone(req.Phone).
		SetRole(authapi.RoleDriver).
		SetStatus("active").
		SetSyncStatus("synced").
		Save(ctx)
}

// sendRiderAccountEmail emails the rider their sign-in details. Best effort: the rider is saved
// either way and the admin saw the temporary password.
func (s *Service) sendRiderAccountEmail(ctx context.Context, tenantID uuid.UUID, tenantSlug string, memberID uuid.UUID, req OnboardRiderRequest, tempPassword string) bool {
	err := s.mail.Send(ctx, tenantID, tenantSlug, "rider-account:"+memberID.String(), notify.Message{
		Channel:  "email",
		Template: riderAccountTemplate,
		To:       []string{req.Email},
		Data: map[string]any{
			"name":          req.FirstName,
			"email":         req.Email,
			"temp_password": tempPassword,
			"login_url":     s.riderAppURL + "/" + tenantSlug,
		},
		Metadata: map[string]any{"subject": "Your rider account is ready"},
	})
	if err != nil {
		s.log.Warn("onboard rider: account email not sent", zap.String("member_id", memberID.String()), zap.Error(err))
		return false
	}
	return true
}
