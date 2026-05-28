package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/consts"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	enttenant "github.com/bengobox/logistics-service/internal/ent/tenant"
	"github.com/bengobox/logistics-service/internal/ent/serviceconfig"
	"github.com/bengobox/logistics-service/internal/ent/user"
	"github.com/bengobox/logistics-service/internal/modules/tenant"
	"github.com/bengobox/logistics-service/internal/platform/events"
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
	publisher    *events.Publisher
	log          *zap.Logger
}

// NewService creates a new Identity Service.
func NewService(client *ent.Client, tenantSyncer *tenant.Syncer, publisher *events.Publisher, log *zap.Logger) *Service {
	if log == nil {
		log = zap.NewNop()
	}
	return &Service{
		client:       client,
		tenantSyncer: tenantSyncer,
		publisher:    publisher,
		log:          log.Named("identity"),
	}
}

// SetPublisher sets the event publisher (called after NATS initialization).
func (s *Service) SetPublisher(p *events.Publisher) {
	s.publisher = p
}

// ResolveTenantSlug syncs and resolves a tenant slug to its UUID.
func (s *Service) ResolveTenantSlug(ctx context.Context, slug string) (uuid.UUID, error) {
	return s.tenantSyncer.SyncTenant(ctx, slug)
}

// EnsureUserFromToken performs JIT (Just-In-Time) provisioning of users and tenants.
// If the user doesn't exist locally, it creates them. If the tenant doesn't exist,
// it syncs it from the auth-service first.
func (s *Service) EnsureUserFromToken(ctx context.Context, authServiceID uuid.UUID, tenantSlug string, claims map[string]any) (*ent.User, error) {
	// 1. Ensure tenant exists first (needed for tenant-scoped lookup)
	tenantID, err := s.tenantSyncer.SyncTenant(ctx, tenantSlug)
	if err != nil {
		return nil, fmt.Errorf("identity.Service: sync tenant %q: %w", tenantSlug, err)
	}

	// 2. Check if user exists by auth_service_id scoped to tenant
	u, err := s.client.User.Query().
		Where(
			user.AuthServiceUserIDEQ(authServiceID),
			user.TenantID(tenantID),
		).
		Only(ctx)

	if err == nil {
		return u, nil
	}

	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("identity.Service: query user: %w", err)
	}

	// 3. Fallback: check if user exists by email (may exist without auth_service_user_id linked)
	email, _ := claims["email"].(string)
	if email != "" {
		existing, emailErr := s.client.User.Query().
			Where(user.EmailEQ(email), user.TenantID(tenantID)).
			Only(ctx)
		if emailErr == nil && existing != nil {
			// Link existing user to auth-service ID
			updated, updErr := existing.Update().
				SetAuthServiceUserID(authServiceID).
				SetSyncStatus("synced").
				SetSyncAt(time.Now()).
				Save(ctx)
			if updErr != nil {
				return nil, fmt.Errorf("identity.Service: link user to auth-service: %w", updErr)
			}
			log.Printf("  [jit-provisioning] linked existing user %s to AuthID %s", email, authServiceID)
			return updated, nil
		}
	}

	// 4. Create user; use auth-service UUID as primary key for cross-service consistency.
	fullName, _ := claims["name"].(string)
	// Fallback: Ent requires non-empty full_name — use email prefix when claim is absent.
	if strings.TrimSpace(fullName) == "" {
		if email != "" {
			fullName = strings.SplitN(email, "@", 2)[0]
		}
		if strings.TrimSpace(fullName) == "" {
			fullName = "User"
		}
	}
	role := roleFromClaims(tenantSlug, claims)

	newUsr, err := s.client.User.Create().
		SetID(authServiceID).
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
// Platform owners with superuser role get "admin".
// Uses "driver" as the universal role for delivery/courier/taxi use cases.
func roleFromClaims(_ string, claims map[string]any) string {
	isPlatformOwner, _ := claims["is_platform_owner"].(bool)
	roles := extractRoles(claims)

	if isPlatformOwner && containsRole(roles, "superuser") {
		return "admin"
	}
	for _, r := range roles {
		switch r {
		case "superuser", "admin":
			return "admin"
		case "staff":
			return "staff"
		case "driver", "rider":
			return RoleDriver
		}
	}
	// Default for fleet members: universal driver role
	return RoleDriver
}

func extractRoles(claims map[string]any) []string {
	switch v := claims["roles"].(type) {
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, r := range v {
			if s, _ := r.(string); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	}
	return nil
}

func containsRole(roles []string, target string) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}
// GetRiderProfile retrieves the user and their associated fleet/vehicle info.
func (s *Service) GetRiderProfile(ctx context.Context, authServiceID, tenantID uuid.UUID) (*ent.User, error) {
	return s.client.User.Query().
		Where(
			user.AuthServiceUserIDEQ(authServiceID),
			user.TenantID(tenantID),
		).
		WithFleetMemberships(func(q *ent.FleetMemberQuery) {
			q.WithVehicle()
		}).
		Only(ctx)
}

// UpdateRiderProfile updates a rider's contact and KYC details.
func (s *Service) UpdateRiderProfile(ctx context.Context, authServiceID, tenantID uuid.UUID, req UpdateRiderProfileRequest) (*ent.User, error) {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	u, err := tx.User.Query().
		Where(
			user.AuthServiceUserIDEQ(authServiceID),
			user.TenantID(tenantID),
		).
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

	// Update KYC fields and transition to pending_review
	now := time.Now()
	err = tx.FleetMember.UpdateOne(fm).
		SetIDNumber(req.IDNumber).
		SetLicenseNo(req.LicenseNo).
		SetIDPassportAttachment(req.IDPassportAttachment).
		SetRiderPhoto(req.RiderPhoto).
		SetStatus("pending_review").
		SetKycSubmittedAt(now).
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

	// Publish KYC submitted event to notify tenant admin
	if s.publisher != nil {
		// Resolve tenant slug from the fleet
		tenantSlug := ""
		if fl, flErr := s.client.Fleet.Get(ctx, fm.FleetID); flErr == nil {
			tenantSlug = fl.TenantSlug
		}
		if pubErr := s.publisher.PublishFleetMemberKYCSubmitted(ctx, tenantID, events.FleetMemberEventData{
			MemberID:   fm.ID.String(), UserID: u.ID.String(),
			FleetID:    fm.FleetID.String(), UserEmail: u.Email, UserName: u.FullName,
			TenantSlug: tenantSlug,
		}); pubErr != nil {
			s.log.Warn("failed to publish fleet.member_kyc_submitted", zap.Error(pubErr))
		}
	}

	return s.GetRiderProfile(ctx, authServiceID, tenantID)
}

// ResolveEnabledModules returns the enabled modules and use_case for a tenant.
// Resolution: explicit ServiceConfig override → use_case defaults → AllModules.
func (s *Service) ResolveEnabledModules(ctx context.Context, tenantID uuid.UUID) (modules []string, useCase string) {
	// 1. Look up tenant's use_case
	t, err := s.client.Tenant.Query().Where(enttenant.IDEQ(tenantID)).Only(ctx)
	if err == nil && t.UseCase != nil {
		useCase = *t.UseCase
	}

	// 2. Check for explicit ServiceConfig override (key: logistics.enabled_modules)
	sc, err := s.client.ServiceConfig.Query().
		Where(
			serviceconfig.ConfigKey("logistics.enabled_modules"),
			serviceconfig.TenantID(tenantID),
		).
		First(ctx)
	if err == nil && sc != nil && sc.ConfigValue != "" {
		var override []string
		if jsonErr := json.Unmarshal([]byte(sc.ConfigValue), &override); jsonErr == nil && len(override) > 0 {
			return override, useCase
		}
	}

	return consts.ResolveModules(useCase, nil), useCase
}
