package identity

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/user"
	"github.com/bengobox/logistics-service/internal/modules/tenant"
)

// Service handles identity-related operations using Ent.
type Service struct {
	client       *ent.Client
	tenantSyncer *tenant.Syncer
}

// NewService creates a new Identity Service.
func NewService(client *ent.Client, tenantSyncer *tenant.Syncer) *Service {
	return &Service{
		client:       client,
		tenantSyncer: tenantSyncer,
	}
}

// EnsureUserFromToken performs JIT (Just-In-Time) provisioning of users and tenants.
// If the user doesn't exist locally, it creates them. If the tenant doesn't exist,
// it syncs it from the auth-service first.
func (s *Service) EnsureUserFromToken(ctx context.Context, authServiceID uuid.UUID, tenantSlug string, claims map[string]any) (*ent.User, error) {
	// 1. Check if user exists by auth_service_id
	u, err := s.client.User.Query().
		Where(user.AuthServiceUserIDEQ(authServiceID)).
		Only(ctx)
	
	if err == nil {
		return u, nil
	}

	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("identity.Service: query user: %w", err)
	}

	// 2. User not found, ensure tenant exists
	tenantID, err := s.tenantSyncer.SyncTenant(ctx, tenantSlug)
	if err != nil {
		return nil, fmt.Errorf("identity.Service: sync tenant %q: %w", tenantSlug, err)
	}

	// 3. Create user; platform admin (codevertex + superuser) gets admin role (all permissions).
	email, _ := claims["email"].(string)
	fullName, _ := claims["name"].(string)
	role := roleFromClaims(tenantSlug, claims)

	newUsr, err := s.client.User.Create().
		SetID(uuid.New()).
		SetAuthServiceUserID(authServiceID).
		SetTenantID(tenantID).
		SetEmail(email).
		SetFullName(fullName).
		SetStatus("active").
		SetSyncStatus("synced").
		SetSyncAt(time.Now()).
		SetRole(role).
		Save(ctx)

	if err != nil {
		return nil, fmt.Errorf("identity.Service: create user: %w", err)
	}

	log.Printf("  [jit-provisioning] created user %s (AuthID %s) for tenant %s role=%s", email, authServiceID, tenantSlug, role)
	return newUsr, nil
}

// roleFromClaims returns the service-level role for JIT-created users. Platform admin (codevertex + superuser) gets "admin" (all permissions).
func roleFromClaims(tenantSlug string, claims map[string]any) string {
	if tenantSlug == "codevertex" && hasSuperuser(claims) {
		return "admin"
	}
	// Default for riders/other tenants; could be derived from claims["roles"] if needed.
	return "rider"
}

func hasSuperuser(claims map[string]any) bool {
	if roles, ok := claims["roles"].([]interface{}); ok {
		for _, r := range roles {
			if s, _ := r.(string); s == "superuser" {
				return true
			}
		}
	}
	return false
}
