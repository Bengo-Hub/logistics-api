package rbac

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/logisticspermission"
	"github.com/bengobox/logistics-service/internal/ent/logisticsrole"
	"github.com/bengobox/logistics-service/internal/ent/userroleassignment"
)

// System roles every tenant has. They are the single definition of who may do what in
// logistics: the seed, runtime provisioning and SSO role mapping all use them. Custom roles
// can still be created per tenant; system roles are reset to these permission sets.
const (
	RoleAdmin      = "admin"
	RoleDispatcher = "dispatcher"
	RoleDriver     = "driver"
)

// SystemRole describes one system role. Permissions nil means every permission.
type SystemRole struct {
	Code        string
	Name        string
	Description string
	Permissions []string
}

// SystemRoles returns the system roles in a fixed order.
func SystemRoles() []SystemRole {
	return []SystemRole{
		{Code: RoleAdmin, Name: "Administrator", Description: "Full logistics access"},
		{
			Code: RoleDispatcher, Name: "Dispatcher",
			Description: "Runs the dispatch board: tasks, riders on shift, live view and cash hand-ins",
			Permissions: []string{
				PermTaskView, PermTaskAdd, PermTaskChange, PermTaskDelete, PermTaskManage,
				PermFleetView, "logistics.vehicles.view", PermZoneView, "logistics.routing.view",
				"logistics.telemetry.view", PermEarningsView,
			},
		},
		{
			Code: RoleDriver, Name: "Driver",
			Description: "Riders and drivers: their own jobs, location and earnings only",
			// tasks.manage lets a rider move their own job along (status, proof of delivery);
			// the dispatcher-only handlers refuse riders, and reads of other tasks are refused
			// unless the rider holds or could claim the job.
			Permissions: []string{
				"logistics.tasks.view_own", "logistics.tasks.change_own", "logistics.tasks.manage_own", PermTaskManage,
				PermZoneView, "logistics.routing.view",
				"logistics.telemetry.add", "logistics.telemetry.manage_own",
				"logistics.earnings.view_own",
			},
		},
	}
}

// SystemRoleForSSO maps an auth-api role name to a logistics system role, or "" when the
// SSO role grants nothing in logistics. Tenant admin and superuser are handled before this
// (they hold every permission).
func SystemRoleForSSO(role string) string {
	switch strings.ToLower(role) {
	case "dispatcher", "delivery_coordinator", "fleet_manager", "manager":
		return RoleDispatcher
	case "driver", "rider", "courier":
		return RoleDriver
	}
	return ""
}

// SystemRolesForSSO maps a token's roles to the distinct logistics system roles they grant.
func SystemRolesForSSO(roles []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range roles {
		if code := SystemRoleForSSO(r); code != "" && !seen[code] {
			seen[code] = true
			out = append(out, code)
		}
	}
	return out
}

// EnsureSystemRoles creates the tenant's system roles if missing and resets their permission
// sets to SystemRoles, so a stale grant (for example riders reading the whole fleet) is
// removed on the next run. Idempotent.
func EnsureSystemRoles(ctx context.Context, client *ent.Client, tenantID uuid.UUID) error {
	all, err := client.LogisticsPermission.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("rbac: list permissions: %w", err)
	}
	if len(all) == 0 {
		return nil // permissions are seeded first; nothing to grant yet
	}
	byCode := make(map[string]uuid.UUID, len(all))
	allIDs := make([]uuid.UUID, 0, len(all))
	for _, p := range all {
		byCode[p.PermissionCode] = p.ID
		allIDs = append(allIDs, p.ID)
	}
	for _, sr := range SystemRoles() {
		want := allIDs
		if sr.Permissions != nil {
			want = want[:0:0]
			for _, code := range sr.Permissions {
				if id, ok := byCode[code]; ok {
					want = append(want, id)
				}
			}
		}
		role, err := client.LogisticsRole.Query().
			Where(logisticsrole.TenantID(tenantID), logisticsrole.RoleCode(sr.Code)).
			Only(ctx)
		switch {
		case ent.IsNotFound(err):
			if _, err := client.LogisticsRole.Create().
				SetTenantID(tenantID).SetRoleCode(sr.Code).SetName(sr.Name).
				SetDescription(sr.Description).SetIsSystemRole(true).
				AddPermissionIDs(want...).Save(ctx); err != nil && !ent.IsConstraintError(err) {
				return fmt.Errorf("rbac: create %s role: %w", sr.Code, err)
			}
		case err != nil:
			return fmt.Errorf("rbac: load %s role: %w", sr.Code, err)
		default:
			if err := role.Update().SetIsSystemRole(true).SetDescription(sr.Description).
				ClearPermissions().AddPermissionIDs(want...).Exec(ctx); err != nil {
				return fmt.Errorf("rbac: reset %s role: %w", sr.Code, err)
			}
		}
	}
	return nil
}

var nowFunc = time.Now

// permissionCodes resolves, in one query, the permission codes of the user's assigned roles
// (not expired) plus the system roles their SSO roles map to.
func permissionCodes(ctx context.Context, client *ent.Client, tenantID, userID uuid.UUID, ssoRoleCodes []string) ([]string, error) {
	match := logisticsrole.HasUserAssignmentsWith(
		userroleassignment.UserID(userID),
		userroleassignment.TenantID(tenantID),
		userroleassignment.Or(userroleassignment.ExpiresAtIsNil(), userroleassignment.ExpiresAtGT(nowFunc())),
	)
	if len(ssoRoleCodes) > 0 {
		match = logisticsrole.Or(match, logisticsrole.RoleCodeIn(ssoRoleCodes...))
	}
	return client.LogisticsRole.Query().
		Where(logisticsrole.TenantID(tenantID), match).
		QueryPermissions().
		Unique(true).
		Select(logisticspermission.FieldPermissionCode).
		Strings(ctx)
}
