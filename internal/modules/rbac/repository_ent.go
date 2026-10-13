package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/logisticspermission"
	"github.com/bengobox/logistics-service/internal/ent/userroleassignment"
	"github.com/google/uuid"
)

// EntRepository implements the Repository interface using Ent ORM.
type EntRepository struct {
	client *ent.Client
}

// NewEntRepository creates a new Ent-backed repository.
func NewEntRepository(client *ent.Client) *EntRepository {
	return &EntRepository{client: client}
}

// GetRole retrieves a role by ID. Roles are platform-wide.
func (r *EntRepository) GetRole(ctx context.Context, roleID uuid.UUID) (*LogisticsRole, error) {
	entRole, err := r.client.LogisticsRole.Get(ctx, roleID)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, fmt.Errorf("role not found: %w", err)
		}
		return nil, fmt.Errorf("get role: %w", err)
	}
	return mapEntRole(entRole), nil
}

// PermissionCodeList returns every permission code in the catalogue.
func (r *EntRepository) PermissionCodeList(ctx context.Context) ([]string, error) {
	return r.client.LogisticsPermission.Query().
		Select(logisticspermission.FieldPermissionCode).
		Strings(ctx)
}

// AssignRoleToUser assigns a role to a user in a tenant.
func (r *EntRepository) AssignRoleToUser(ctx context.Context, tenantID uuid.UUID, assignment *UserRoleAssignment) error {
	if assignment == nil {
		return errors.New("assignment cannot be nil")
	}
	builder := r.client.UserRoleAssignment.Create().
		SetID(assignment.ID).
		SetTenantID(tenantID).
		SetUserID(assignment.UserID).
		SetRoleID(assignment.RoleID).
		SetAssignedBy(assignment.AssignedBy)
	if assignment.ExpiresAt != nil {
		builder.SetExpiresAt(*assignment.ExpiresAt)
	}
	if _, err := builder.Save(ctx); err != nil {
		return fmt.Errorf("assign role to user: %w", err)
	}
	return nil
}

// RevokeRoleFromUser revokes a role from a user in a tenant.
func (r *EntRepository) RevokeRoleFromUser(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID, roleID uuid.UUID) error {
	_, err := r.client.UserRoleAssignment.Delete().
		Where(
			userroleassignment.TenantID(tenantID),
			userroleassignment.UserID(userID),
			userroleassignment.RoleID(roleID),
		).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("revoke role from user: %w", err)
	}
	return nil
}

// GetUserRoles retrieves the roles assigned to a user in a tenant.
func (r *EntRepository) GetUserRoles(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) ([]*LogisticsRole, error) {
	entRoles, err := r.client.UserRoleAssignment.Query().
		Where(
			userroleassignment.TenantID(tenantID),
			userroleassignment.UserID(userID),
		).
		QueryRole().
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("get user roles: %w", err)
	}
	roles := make([]*LogisticsRole, len(entRoles))
	for i, entRole := range entRoles {
		roles[i] = mapEntRole(entRole)
	}
	return roles, nil
}

// ListUserAssignments lists a tenant's role assignments with optional filters.
func (r *EntRepository) ListUserAssignments(ctx context.Context, tenantID uuid.UUID, filters AssignmentFilters) ([]*UserRoleAssignment, error) {
	query := r.client.UserRoleAssignment.Query().
		Where(userroleassignment.TenantID(tenantID))
	if filters.UserID != nil {
		query = query.Where(userroleassignment.UserID(*filters.UserID))
	}
	if filters.RoleID != nil {
		query = query.Where(userroleassignment.RoleID(*filters.RoleID))
	}
	entAssignments, err := query.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("list user assignments: %w", err)
	}
	assignments := make([]*UserRoleAssignment, len(entAssignments))
	for i, a := range entAssignments {
		assignments[i] = mapEntAssignment(a)
	}
	return assignments, nil
}

func mapEntRole(entRole *ent.LogisticsRole) *LogisticsRole {
	role := &LogisticsRole{
		ID:           entRole.ID,
		RoleCode:     entRole.RoleCode,
		Name:         entRole.Name,
		IsSystemRole: entRole.IsSystemRole,
		CreatedAt:    entRole.CreatedAt,
		UpdatedAt:    entRole.UpdatedAt,
	}
	if entRole.Description != "" {
		role.Description = &entRole.Description
	}
	return role
}

func mapEntAssignment(entAssignment *ent.UserRoleAssignment) *UserRoleAssignment {
	assignment := &UserRoleAssignment{
		ID:         entAssignment.ID,
		TenantID:   entAssignment.TenantID,
		UserID:     entAssignment.UserID,
		RoleID:     entAssignment.RoleID,
		AssignedBy: entAssignment.AssignedBy,
		AssignedAt: entAssignment.AssignedAt,
	}
	if !entAssignment.ExpiresAt.IsZero() {
		assignment.ExpiresAt = &entAssignment.ExpiresAt
	}
	return assignment
}

// PermissionCodes implements Repository.
func (r *EntRepository) PermissionCodes(ctx context.Context, tenantID, userID uuid.UUID, systemRoles []string) ([]string, error) {
	return permissionCodes(ctx, r.client, tenantID, userID, systemRoles)
}

// EnsureSystemRoles implements Repository.
func (r *EntRepository) EnsureSystemRoles(ctx context.Context) error {
	return EnsureSystemRoles(ctx, r.client)
}
