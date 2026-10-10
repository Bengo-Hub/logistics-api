package rbac

import (
	"context"

	"github.com/google/uuid"
)

// Repository abstracts persistence for RBAC entities.
type Repository interface {
	// Role operations
	CreateRole(ctx context.Context, tenantID uuid.UUID, role *LogisticsRole) error
	GetRole(ctx context.Context, tenantID uuid.UUID, roleID uuid.UUID) (*LogisticsRole, error)
	GetRoleByCode(ctx context.Context, tenantID uuid.UUID, roleCode string) (*LogisticsRole, error)
	ListRoles(ctx context.Context, tenantID uuid.UUID) ([]*LogisticsRole, error)

	// Permission operations
	CreatePermission(ctx context.Context, permission *LogisticsPermission) error
	GetPermission(ctx context.Context, permissionID uuid.UUID) (*LogisticsPermission, error)
	GetPermissionByCode(ctx context.Context, permissionCode string) (*LogisticsPermission, error)
	ListPermissions(ctx context.Context, filters PermissionFilters) ([]*LogisticsPermission, error)

	// Role-Permission operations
	AssignPermissionToRole(ctx context.Context, roleID uuid.UUID, permissionID uuid.UUID) error
	RemovePermissionFromRole(ctx context.Context, roleID uuid.UUID, permissionID uuid.UUID) error
	GetRolePermissions(ctx context.Context, roleID uuid.UUID) ([]*LogisticsPermission, error)

	// User-Role assignment operations
	AssignRoleToUser(ctx context.Context, tenantID uuid.UUID, assignment *UserRoleAssignment) error
	RevokeRoleFromUser(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleID uuid.UUID) error
	GetUserRoles(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*LogisticsRole, error)
	GetUserPermissions(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*LogisticsPermission, error)
	ListUserAssignments(ctx context.Context, tenantID uuid.UUID, filters AssignmentFilters) ([]*UserRoleAssignment, error)

	// PermissionCodes returns, in one query, the permission codes of the user's assigned roles
	// plus the named system roles (mapped from SSO roles).
	PermissionCodes(ctx context.Context, tenantID, userID uuid.UUID, systemRoles []string) ([]string, error)
	// EnsureSystemRoles creates the tenant's system roles and resets their permission sets.
	EnsureSystemRoles(ctx context.Context, tenantID uuid.UUID) error
}

// PermissionFilters for listing permissions.
type PermissionFilters struct {
	Module *string
	Action *string
}

// AssignmentFilters for listing role assignments.
type AssignmentFilters struct {
	UserID *uuid.UUID
	RoleID *uuid.UUID
}
