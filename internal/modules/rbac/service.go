package rbac

import (
	"context"
	"fmt"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/tenant"
)

// Service provides business logic for RBAC operations.
type Service struct {
	repo         Repository
	logger       *zap.Logger
	tenantSyncer *tenant.Syncer
}

// NewService creates a new RBAC service.
func NewService(repo Repository, logger *zap.Logger, tenantSyncer *tenant.Syncer) *Service {
	return &Service{
		repo:         repo,
		logger:       logger,
		tenantSyncer: tenantSyncer,
	}
}

// HasPermission checks if a user has a specific permission. Tenant admins and superusers
// (from the request's token) hold every permission; other users hold the permissions of
// their assigned roles plus the system roles their SSO roles map to (a "dispatcher" in auth
// is a dispatcher here without any role row).
func (s *Service) HasPermission(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, permissionCode string) (bool, error) {
	codes, full, err := s.EffectivePermissions(ctx, tenantID, userID)
	if err != nil {
		return false, err
	}
	if full {
		return true, nil
	}
	for _, c := range codes {
		if c == permissionCode {
			return true, nil
		}
	}
	return false, nil
}

// EffectivePermissions returns the caller's permission codes. full is true for tenant
// admins, superusers and platform owners, who hold every permission.
func (s *Service) EffectivePermissions(ctx context.Context, tenantID, userID uuid.UUID) (codes []string, full bool, err error) {
	var ssoRoles []string
	if claims, ok := authclient.ClaimsFromContext(ctx); ok && claims != nil {
		if claims.IsPlatformOwner || claims.IsSuperuser() || claims.IsAdmin() {
			return nil, true, nil
		}
		ssoRoles = claims.Roles
	}
	codes, err = s.repo.PermissionCodes(ctx, tenantID, userID, SystemRolesForSSO(ssoRoles))
	if err != nil {
		return nil, false, fmt.Errorf("get user permissions: %w", err)
	}
	return codes, false, nil
}

// EnsureSystemRoles creates the tenant's admin, dispatcher and driver roles if missing and
// resets their permission sets. Called by the seed for every tenant and when a tenant first
// needs a role at runtime.
func (s *Service) EnsureSystemRoles(ctx context.Context, tenantID uuid.UUID) error {
	return s.repo.EnsureSystemRoles(ctx, tenantID)
}

// GetUserRoles returns all roles assigned to a user in a tenant.
func (s *Service) GetUserRoles(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*LogisticsRole, error) {
	return s.repo.GetUserRoles(ctx, tenantID, userID)
}

// GetUserPermissions returns all permissions granted to a user in a tenant (via their roles).
func (s *Service) GetUserPermissions(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*LogisticsPermission, error) {
	return s.repo.GetUserPermissions(ctx, tenantID, userID)
}

// HasRole checks if a user has a specific role.
func (s *Service) HasRole(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleCode string) (bool, error) {
	roles, err := s.repo.GetUserRoles(ctx, tenantID, userID)
	if err != nil {
		return false, fmt.Errorf("get user roles: %w", err)
	}

	for _, role := range roles {
		if role.RoleCode == roleCode {
			return true, nil
		}
	}

	return false, nil
}

// AssignRole assigns a role to a user.
func (s *Service) AssignRole(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleID uuid.UUID, assignedBy uuid.UUID) error {
	// The role must be one of this tenant's roles; never another tenant's.
	if _, err := s.repo.GetRole(ctx, tenantID, roleID); err != nil {
		return fmt.Errorf("role not found for this tenant")
	}
	// Check if assignment already exists
	assignments, err := s.repo.ListUserAssignments(ctx, tenantID, AssignmentFilters{
		UserID: &userID,
		RoleID: &roleID,
	})
	if err != nil {
		return fmt.Errorf("check existing assignment: %w", err)
	}

	if len(assignments) > 0 {
		return fmt.Errorf("role already assigned to user")
	}

	assignment := &UserRoleAssignment{
		ID:         uuid.New(),
		TenantID:   tenantID,
		UserID:     userID,
		RoleID:     roleID,
		AssignedBy: assignedBy,
	}

	if err := s.repo.AssignRoleToUser(ctx, tenantID, assignment); err != nil {
		return fmt.Errorf("assign role: %w", err)
	}

	s.logger.Info("role assigned",
		zap.String("tenant_id", tenantID.String()),
		zap.String("user_id", userID.String()),
		zap.String("role_id", roleID.String()),
		zap.String("assigned_by", assignedBy.String()),
	)

	return nil
}

// RevokeRole revokes a role from a user.
func (s *Service) RevokeRole(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleID uuid.UUID) error {
	if err := s.repo.RevokeRoleFromUser(ctx, tenantID, userID, roleID); err != nil {
		return fmt.Errorf("revoke role: %w", err)
	}

	s.logger.Info("role revoked",
		zap.String("tenant_id", tenantID.String()),
		zap.String("user_id", userID.String()),
		zap.String("role_id", roleID.String()),
	)

	return nil
}


// AllPermissionCodes returns every logistics permission code. Tenant admins and platform
// owners hold all of them, so /auth/me reports the full list for them.
func (s *Service) AllPermissionCodes(ctx context.Context) ([]string, error) {
	perms, err := s.repo.ListPermissions(ctx, PermissionFilters{})
	if err != nil {
		return nil, fmt.Errorf("list permissions: %w", err)
	}
	codes := make([]string, 0, len(perms))
	for _, p := range perms {
		codes = append(codes, p.PermissionCode)
	}
	return codes, nil
}
