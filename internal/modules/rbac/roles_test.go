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
