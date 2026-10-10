package rbac

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/logisticspermission"
	"github.com/bengobox/logistics-service/internal/ent/logisticsrole"
	"github.com/bengobox/logistics-service/internal/ent/user"
	"github.com/bengobox/logistics-service/internal/ent/userroleassignment"
)

// Read models for the Roles page. Lists use a fixed number of queries whatever the number
// of roles or assignments (no query per row).

// RoleView is a role with its permission codes and how many users hold it.
type RoleView struct {
	ID              uuid.UUID `json:"id"`
	RoleCode        string    `json:"role_code"`
	Name            string    `json:"name"`
	Description     string    `json:"description,omitempty"`
	IsSystemRole    bool      `json:"is_system_role"`
	Permissions     []string  `json:"permissions"`
	AssignmentCount int       `json:"assignment_count"`
}

// PermissionView is one permission in the catalogue.
type PermissionView struct {
	ID             uuid.UUID `json:"id"`
	PermissionCode string    `json:"permission_code"`
	Name           string    `json:"name"`
	Module         string    `json:"module"`
	Action         string    `json:"action"`
}

// AssignmentView is a role held by a user, with names for display.
type AssignmentView struct {
	ID         uuid.UUID  `json:"id"`
	UserID     uuid.UUID  `json:"user_id"`
	UserName   string     `json:"user_name"`
	UserEmail  string     `json:"user_email"`
	RoleID     uuid.UUID  `json:"role_id"`
	RoleCode   string     `json:"role_code"`
	RoleName   string     `json:"role_name"`
	AssignedAt time.Time  `json:"assigned_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

// Assignment errors the handler maps to 409 and 400.
var (
	ErrAlreadyAssigned = fmt.Errorf("rbac: the user already has this role")
	ErrRoleNotInTenant = fmt.Errorf("rbac: role not found for this tenant")
)

// ListRoleViews returns the tenant's roles, system roles first.
func ListRoleViews(ctx context.Context, client *ent.Client, tenantID uuid.UUID) ([]RoleView, error) {
	roles, err := client.LogisticsRole.Query().
		Where(logisticsrole.TenantID(tenantID)).
		WithPermissions(func(q *ent.LogisticsPermissionQuery) {
			q.Select(logisticspermission.FieldPermissionCode)
		}).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("rbac: list roles: %w", err)
	}
	var counts []struct {
		RoleID uuid.UUID `json:"role_id"`
		Count  int       `json:"count"`
	}
	if err := client.UserRoleAssignment.Query().
		Where(userroleassignment.TenantID(tenantID)).
		GroupBy(userroleassignment.FieldRoleID).
		Aggregate(ent.Count()).
		Scan(ctx, &counts); err != nil {
		return nil, fmt.Errorf("rbac: count assignments: %w", err)
	}
	byRole := make(map[uuid.UUID]int, len(counts))
	for _, c := range counts {
		byRole[c.RoleID] = c.Count
	}
	out := make([]RoleView, 0, len(roles))
	for _, r := range roles {
		codes := make([]string, 0, len(r.Edges.Permissions))
		for _, p := range r.Edges.Permissions {
			codes = append(codes, p.PermissionCode)
		}
		sort.Strings(codes)
		out = append(out, RoleView{
			ID: r.ID, RoleCode: r.RoleCode, Name: r.Name, Description: r.Description,
			IsSystemRole: r.IsSystemRole, Permissions: codes, AssignmentCount: byRole[r.ID],
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsSystemRole != out[j].IsSystemRole {
			return out[i].IsSystemRole
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// ListPermissionViews returns the permission catalogue ordered by module and action.
func ListPermissionViews(ctx context.Context, client *ent.Client) ([]PermissionView, error) {
	perms, err := client.LogisticsPermission.Query().
		Order(ent.Asc(logisticspermission.FieldModule), ent.Asc(logisticspermission.FieldAction)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("rbac: list permissions: %w", err)
	}
	out := make([]PermissionView, 0, len(perms))
	for _, p := range perms {
		out = append(out, PermissionView{ID: p.ID, PermissionCode: p.PermissionCode, Name: p.Name, Module: p.Module, Action: p.Action})
	}
	return out, nil
}

// ListAssignmentViews returns the tenant's role assignments, optionally for one user.
func ListAssignmentViews(ctx context.Context, client *ent.Client, tenantID uuid.UUID, userID *uuid.UUID) ([]AssignmentView, error) {
	q := client.UserRoleAssignment.Query().
		Where(userroleassignment.TenantID(tenantID)).
		WithRole().
		WithUser(func(uq *ent.UserQuery) { uq.Select(user.FieldFullName, user.FieldEmail) }).
		Order(ent.Desc(userroleassignment.FieldAssignedAt))
	if userID != nil {
		q = q.Where(userroleassignment.UserID(*userID))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("rbac: list assignments: %w", err)
	}
	out := make([]AssignmentView, 0, len(rows))
	for _, a := range rows {
		v := AssignmentView{ID: a.ID, UserID: a.UserID, RoleID: a.RoleID, AssignedAt: a.AssignedAt}
		if !a.ExpiresAt.IsZero() {
			exp := a.ExpiresAt
			v.ExpiresAt = &exp
		}
		if r := a.Edges.Role; r != nil {
			v.RoleCode, v.RoleName = r.RoleCode, r.Name
		}
		if u := a.Edges.User; u != nil {
			v.UserName, v.UserEmail = u.FullName, u.Email
		}
		out = append(out, v)
	}
	return out, nil
}
