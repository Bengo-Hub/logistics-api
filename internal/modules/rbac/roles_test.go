package rbac

import (
	"context"
	"testing"

	authclient "github.com/Bengo-Hub/shared-auth-client"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type stubRepo struct {
	Repository
	assigned []string            // codes from role assignments
	byRole   map[string][]string // codes per system role
	gotRoles []string
}

func (r *stubRepo) PermissionCodes(_ context.Context, _, _ uuid.UUID, roles []string) ([]string, error) {
	r.gotRoles = roles
	out := append([]string{}, r.assigned...)
	for _, role := range roles {
		out = append(out, r.byRole[role]...)
	}
	return out, nil
}

func ctxWith(roles ...string) context.Context {
	c := &authclient.Claims{Roles: roles}
	return authclient.ContextWithClaims(context.Background(), c)
}

func TestHasPermission(t *testing.T) {
	repo := &stubRepo{byRole: map[string][]string{
		RoleDispatcher: {PermTaskAdd, PermFleetView},
		RoleDriver:     {"logistics.tasks.manage_own"},
	}}
	svc := NewService(repo, zap.NewNop(), nil)
	tenant, user := uuid.New(), uuid.New()

	if ok, _ := svc.HasPermission(ctxWith("admin"), tenant, user, PermConfigManage); !ok {
		t.Fatal("tenant admin must hold every permission")
	}
	if ok, _ := svc.HasPermission(ctxWith("delivery_coordinator"), tenant, user, PermTaskAdd); !ok {
		t.Fatal("an SSO delivery coordinator must get the dispatcher role")
	}
	if ok, _ := svc.HasPermission(ctxWith("rider"), tenant, user, PermFleetView); ok {
		t.Fatal("riders must not read the fleet")
	}
	if ok, _ := svc.HasPermission(ctxWith("customer"), tenant, user, PermTaskView); ok {
		t.Fatal("unrelated SSO roles grant nothing")
	}
	repo.assigned = []string{PermZoneManage}
	if ok, _ := svc.HasPermission(ctxWith("customer"), tenant, user, PermZoneManage); !ok {
		t.Fatal("assigned roles still count")
	}
}

func TestSystemRoleDefinitions(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range SystemRoles() {
		seen[r.Code] = true
		if r.Code == RoleDriver {
			for _, p := range r.Permissions {
				if p == PermFleetView || p == PermTaskAdd || p == PermTaskView {
					t.Fatalf("driver role must not hold %s", p)
				}
			}
		}
	}
	for _, c := range []string{RoleAdmin, RoleDispatcher, RoleDriver} {
		if !seen[c] {
			t.Fatalf("missing system role %s", c)
		}
	}
	if got := SystemRolesForSSO([]string{"rider", "driver", "manager"}); len(got) != 2 {
		t.Fatalf("roles must be distinct: %v", got)
	}
}

// Marking a delivery done without the customer's code is for admins only: dispatchers confirm
// with the code and riders never close a job without it.
func TestDeliveryOverrideIsAdminOnly(t *testing.T) {
	for _, r := range SystemRoles() {
		if r.Code == RoleAdmin {
			if r.Permissions != nil {
				t.Fatal("admin role must hold every permission, including the override")
			}
			continue
		}
		for _, p := range r.Permissions {
			if p == PermTaskOverride {
				t.Fatalf("%s must not hold %s", r.Code, PermTaskOverride)
			}
		}
	}
	repo := &stubRepo{byRole: map[string][]string{
		RoleDispatcher: SystemRoles()[1].Permissions,
		RoleDriver:     SystemRoles()[2].Permissions,
	}}
	svc := NewService(repo, zap.NewNop(), nil)
	tenant, user := uuid.New(), uuid.New()
	if ok, _ := svc.HasPermission(ctxWith("admin"), tenant, user, PermTaskOverride); !ok {
		t.Fatal("tenant admin must be able to override")
	}
	for _, role := range []string{"dispatcher", "rider"} {
		if ok, _ := svc.HasPermission(ctxWith(role), tenant, user, PermTaskOverride); ok {
			t.Fatalf("%s must not be able to override", role)
		}
	}
}
