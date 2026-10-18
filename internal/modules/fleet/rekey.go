package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/ent/userroleassignment"
)

// RekeyFields overrides the copied profile when a user is moved onto its auth id. Empty values
// keep what the old row had.
type RekeyFields struct {
	FullName string
	Phone    string
	Role     string
}

// RekeyUser moves a local user (an invite or onboarding stub created before the auth account
// existed, with a random primary key) onto the auth user id, which is the key RBAC and every
// other service use. The rider's fleet membership, role grants and tasks follow. It runs inside
// tx; the caller commits.
//
// The old row's email and auth link are moved aside first, because users are unique on
// (tenant_id, email) and on auth_service_user_id: creating the new row while the old one still
// held them failed, which left invited riders linked under the random key and their role grants
// unreachable.
//
// This is the one re-key path (onboarding, auth.user.created, first sign-in).
func RekeyUser(ctx context.Context, tx *ent.Tx, old *ent.User, authID uuid.UUID, f RekeyFields) (*ent.User, error) {
	if old.ID == authID {
		return old, nil
	}
	pick := func(override, current string) string {
		if override != "" {
			return override
		}
		return current
	}
	if err := tx.User.UpdateOne(old).
		SetEmail(fmt.Sprintf("rekeyed-%s@invalid.local", old.ID)).
		ClearAuthServiceUserID().
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("fleet: rekey user: free email: %w", err)
	}
	b := tx.User.Create().
		SetID(authID).
		SetAuthServiceUserID(authID).
		SetTenantID(old.TenantID).
		SetEmail(old.Email).
		SetFullName(pick(f.FullName, old.FullName)).
		SetStatus("active").
		SetSyncStatus("synced").
		SetSyncAt(time.Now()).
		SetMetadata(old.Metadata)
	if phone := pick(f.Phone, old.Phone); phone != "" {
		b.SetPhone(phone)
	}
	if role := pick(f.Role, old.Role); role != "" {
		b.SetRole(role)
	}
	nu, err := b.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: rekey user: create under auth id: %w", err)
	}
	if err := tx.FleetMember.Update().
		Where(fleetmember.UserID(old.ID)).
		SetUserID(authID).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("fleet: rekey user: repoint fleet members: %w", err)
	}
	if err := tx.UserRoleAssignment.Update().
		Where(userroleassignment.UserID(old.ID)).
		SetUserID(authID).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("fleet: rekey user: repoint role grants: %w", err)
	}
	taskIDs, err := tx.User.QueryTasks(old).IDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("fleet: rekey user: tasks: %w", err)
	}
	if len(taskIDs) > 0 {
		if err := tx.User.UpdateOneID(authID).AddTaskIDs(taskIDs...).Exec(ctx); err != nil {
			return nil, fmt.Errorf("fleet: rekey user: repoint tasks: %w", err)
		}
	}
	if err := tx.User.DeleteOneID(old.ID).Exec(ctx); err != nil {
		return nil, fmt.Errorf("fleet: rekey user: delete old row: %w", err)
	}
	return nu, nil
}
