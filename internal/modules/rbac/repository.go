package rbac

import (
	"context"

	"github.com/google/uuid"
)

// Repository abstracts persistence for RBAC entities. Roles and permissions are platform-wide
// (the same for every tenant); only user role assignments carry a tenant.
type Repository interface {
	// GetRole returns a role by id.
	GetRole(ctx context.Context, roleID uuid.UUID) (*LogisticsRole, error)
	// PermissionCodeList returns every permission code in the catalogue.
	PermissionCodeList(ctx context.Context) ([]string, error)

	// User role assignments (tenant-scoped).
	AssignRoleToUser(ctx context.Context, tenantID uuid.UUID, assignment *UserRoleAssignment) error
	RevokeRoleFromUser(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleID uuid.UUID) error
	GetUserRoles(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*LogisticsRole, error)
	ListUserAssignments(ctx context.Context, tenantID uuid.UUID, filters AssignmentFilters) ([]*UserRoleAssignment, error)

	// PermissionCodes returns, in one query, the permission codes of the user's assigned roles
	// in the tenant plus the named system roles (mapped from SSO roles).
	PermissionCodes(ctx context.Context, tenantID, userID uuid.UUID, systemRoles []string) ([]string, error)
	// EnsureSystemRoles creates the system roles if missing and resets their permission sets.
	EnsureSystemRoles(ctx context.Context) error
}

// AssignmentFilters for listing role assignments.
type AssignmentFilters struct {
	UserID *uuid.UUID
	RoleID *uuid.UUID
}
