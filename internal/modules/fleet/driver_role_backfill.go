package fleet

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/logisticsrole"
	"github.com/bengobox/logistics-service/internal/ent/userroleassignment"
	"github.com/bengobox/logistics-service/internal/modules/rbac"
)

// BackfillDriverRoles gives every active rider the driver role in their tenant when they lack it.
// Activation grants it (ensureDriverRole), but riders activated before that existed, or whose
// grant failed, could not move their own jobs along (PATCH status needs logistics.tasks.manage).
// It only adds missing rows, so running it on every seed is safe. Returns how many it added.
func BackfillDriverRoles(ctx context.Context, client *ent.Client) (int, error) {
	role, err := client.LogisticsRole.Query().
		Where(logisticsrole.RoleCode(rbac.RoleDriver)).
		Only(ctx)
	if err != nil {
		return 0, fmt.Errorf("fleet: driver role: %w", err)
	}

	members, err := client.FleetMember.Query().
		Where(fleetmember.Status("active")).
		Select(fleetmember.FieldTenantID, fleetmember.FieldUserID).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("fleet: list active riders: %w", err)
	}

	type key struct{ tenant, user uuid.UUID }
	have := map[key]bool{}
	existing, err := client.UserRoleAssignment.Query().
		Where(userroleassignment.RoleID(role.ID)).
		Select(userroleassignment.FieldTenantID, userroleassignment.FieldUserID).
		All(ctx)
	if err != nil {
		return 0, fmt.Errorf("fleet: list driver grants: %w", err)
	}
	for _, a := range existing {
		have[key{a.TenantID, a.UserID}] = true
	}

	added := 0
	for _, m := range members {
		k := key{m.TenantID, m.UserID}
		if m.UserID == uuid.Nil || have[k] {
			continue
		}
		have[k] = true // a rider in two fleets of one tenant gets one grant
		if _, err := client.UserRoleAssignment.Create().
			SetTenantID(m.TenantID).
			SetUserID(m.UserID).
			SetRoleID(role.ID).
			SetAssignedBy(m.UserID). // same convention as ensureDriverRole: no acting admin
			Save(ctx); err != nil {
			if ent.IsConstraintError(err) {
				continue // granted in parallel
			}
			return added, fmt.Errorf("fleet: grant driver role to %s: %w", m.UserID, err)
		}
		added++
	}
	return added, nil
}
