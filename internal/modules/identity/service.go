package identity

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/user"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/modules/tenant"
)

// UpdateRiderProfileRequest defines the fields expected for profile updates.
type UpdateRiderProfileRequest struct {
	Phone        string `json:"phone"`
	VehicleType  string `json:"vehicle_type"`
	LicenseNo    string `json:"license_no"`
	LicensePlate string `json:"license_plate"`
	IDNumber              string `json:"id_number"`
	IDPassportAttachment  string `json:"id_passport_attachment"`
	RiderPhoto            string `json:"rider_photo"`
	ImageLicensePlate     string `json:"image_license_plate"`
	ImageSideView         string `json:"image_side_view"`
}

// Service handles identity-related operations using Ent.
type Service struct {
	client       *ent.Client
	tenantSyncer *tenant.Syncer
}

// NewService creates a new Identity Service.
func NewService(client *ent.Client, tenantSyncer *tenant.Syncer) *Service {
	return &Service{
		client:       client,
		tenantSyncer: tenantSyncer,
	}
}

// EnsureUserFromToken performs JIT (Just-In-Time) provisioning of users and tenants.
// If the user doesn't exist locally, it creates them. If the tenant doesn't exist,
// it syncs it from the auth-service first.
func (s *Service) EnsureUserFromToken(ctx context.Context, authServiceID uuid.UUID, tenantSlug string, claims map[string]any) (*ent.User, error) {
	// 1. Check if user exists by auth_service_id
	u, err := s.client.User.Query().
		Where(user.AuthServiceUserIDEQ(authServiceID)).
		Only(ctx)
	
	if err == nil {
		return u, nil
	}

	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("identity.Service: query user: %w", err)
	}

	// 2. User not found, ensure tenant exists
	tenantID, err := s.tenantSyncer.SyncTenant(ctx, tenantSlug)
	if err != nil {
		return nil, fmt.Errorf("identity.Service: sync tenant %q: %w", tenantSlug, err)
	}

	// 3. Create user; platform admin (codevertex + superuser) gets admin role (all permissions).
	email, _ := claims["email"].(string)
	fullName, _ := claims["name"].(string)
	role := roleFromClaims(tenantSlug, claims)

	newUsr, err := s.client.User.Create().
		SetID(uuid.New()).
		SetAuthServiceUserID(authServiceID).
		SetTenantID(tenantID).
		SetEmail(email).
		SetFullName(fullName).
		SetStatus("active").
		SetSyncStatus("synced").
		SetSyncAt(time.Now()).
		SetRole(role).
		Save(ctx)

	if err != nil {
		return nil, fmt.Errorf("identity.Service: create user: %w", err)
	}

	log.Printf("  [jit-provisioning] created user %s (AuthID %s) for tenant %s role=%s", email, authServiceID, tenantSlug, role)
	return newUsr, nil
}

// roleFromClaims returns the service-level role for JIT-created users.
// Platform admin (codevertex + superuser) gets "admin".
// Uses "driver" as the universal role for delivery/courier/taxi use cases.
func roleFromClaims(tenantSlug string, claims map[string]any) string {
	if tenantSlug == "codevertex" && hasSuperuser(claims) {
		return "admin"
	}
	// Extract role from JWT claims if available
	if roles, ok := claims["roles"].([]interface{}); ok {
		for _, r := range roles {
			if s, _ := r.(string); s != "" {
				switch s {
				case "superuser", "admin":
					return "admin"
				case "staff":
					return "staff"
				case "driver", "rider":
					return RoleDriver
				}
			}
		}
	}
	// Default for fleet members: universal driver role
	return RoleDriver
}

func hasSuperuser(claims map[string]any) bool {
	if roles, ok := claims["roles"].([]interface{}); ok {
		for _, r := range roles {
			if s, _ := r.(string); s == "superuser" {
				return true
			}
		}
	}
	return false
}
// GetRiderProfile retrieves the user and their associated fleet/vehicle info.
func (s *Service) GetRiderProfile(ctx context.Context, authServiceID uuid.UUID) (*ent.User, error) {
	return s.client.User.Query().
		Where(user.AuthServiceUserIDEQ(authServiceID)).
		WithFleetMemberships(func(q *ent.FleetMemberQuery) {
			q.WithVehicle()
		}).
		Only(ctx)
}

// UpdateRiderProfile updates a rider's contact and KYC details.
func (s *Service) UpdateRiderProfile(ctx context.Context, authServiceID uuid.UUID, req UpdateRiderProfileRequest) (*ent.User, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	u, err := tx.User.Query().
		Where(user.AuthServiceUserIDEQ(authServiceID)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("user not found: %w", err)
	}

	// 1. Update User Phone
	err = tx.User.UpdateOne(u).
		SetPhone(req.Phone).
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("update user phone: %w", err)
	}

	// 2. Ensure FleetMember exists
	fm, err := tx.FleetMember.Query().
		Where(fleetmember.UserIDEQ(u.ID)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("rider record not found: %w", err)
	}

	// Update KYC fields
	err = tx.FleetMember.UpdateOne(fm).
		SetIDNumber(req.IDNumber).
		SetLicenseNo(req.LicenseNo).
		SetIDPassportAttachment(req.IDPassportAttachment).
		SetRiderPhoto(req.RiderPhoto).
		SetStatus("pending").
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("update fleet member: %w", err)
	}

	// 3. Update or Create Vehicle
	if fm.VehicleID != nil {
		err = tx.Vehicle.UpdateOneID(*fm.VehicleID).
			SetVehicleType(req.VehicleType).
			SetLicensePlate(req.LicensePlate).
			SetImageLicensePlate(req.ImageLicensePlate).
			SetImageSideView(req.ImageSideView).
			SetComplianceStatus("pending").
			Exec(ctx)
	} else {
		v, err := tx.Vehicle.Create().
			SetTenantID(u.TenantID).
			SetVehicleType(req.VehicleType).
			SetLicensePlate(req.LicensePlate).
			SetImageLicensePlate(req.ImageLicensePlate).
			SetImageSideView(req.ImageSideView).
			SetComplianceStatus("pending").
			Save(ctx)
		if err == nil {
			err = tx.FleetMember.UpdateOne(fm).SetVehicleID(v.ID).Exec(ctx)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("update vehicle: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return s.GetRiderProfile(ctx, authServiceID)
}
