package tenant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent"
	enttenant "github.com/bengobox/logistics-service/internal/ent/tenant"
)

// ErrNotEntitled means the tenant's subscription does not include logistics, so it is not
// provisioned here.
var ErrNotEntitled = errors.New("tenant: logistics is not part of this subscription")

// EntitlementGate reports whether a tenant's subscription includes logistics.
type EntitlementGate func(ctx context.Context, tenantID string) bool

// Syncer handles dynamic syncing of tenant data from auth-api using Ent ORM.
type Syncer struct {
	client  *ent.Client
	authURL string
	gate    EntitlementGate
}

// SetEntitlementGate makes the syncer refuse tenants whose plan has no logistics. Tenants
// already mirrored are returned as before.
func (s *Syncer) SetEntitlementGate(g EntitlementGate) { s.gate = g }

// Entitled runs the gate (true when none is wired).
func (s *Syncer) Entitled(ctx context.Context, tenantID string) bool {
	return s.gate == nil || s.gate(ctx, tenantID)
}

// NewSyncer creates a new TenantSyncer.
// authURL is the base URL of the auth-api (e.g. from AUTH_SERVICE_URL config).
func NewSyncer(client *ent.Client, authURL string) *Syncer {
	return &Syncer{client: client, authURL: authURL}
}

// authAPITenantResponse is the minimal tenant JSON response from GET /api/v1/tenants/by-slug/{slug}.
// Only fields that this service persists locally are included.
type authAPITenantResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Status  string `json:"status"`
	UseCase string `json:"use_case"`
}

// SyncTenant fetches the tenant record from auth-api and persists the minimal
// reference in the local PG DB using Ent.
func (s *Syncer) SyncTenant(ctx context.Context, slug string) (uuid.UUID, error) {
	// Fast path: check if tenant exists locally
	existing, err := s.client.Tenant.Query().Where(enttenant.SlugEQ(slug)).Only(ctx)
	if err == nil && existing != nil {
		return existing.ID, nil
	}

	authAPIURL := s.authURL
	if envURL := os.Getenv("AUTH_API_URL"); envURL != "" {
		authAPIURL = envURL
	}
	endpoint := strings.TrimRight(authAPIURL, "/") + "/api/v1/tenants/by-slug/" + slug

	log.Printf("  [tenant-sync] dynamically fetching %s from %s", slug, endpoint)
	resp, err := http.Get(endpoint) //nolint:noctx
	if err != nil {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: tenant %q not found (404)", slug)
	}
	if resp.StatusCode != http.StatusOK {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: auth-api HTTP %d for %q", resp.StatusCode, slug)
	}

	var remote authAPITenantResponse
	if err := json.NewDecoder(resp.Body).Decode(&remote); err != nil {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: decode response: %w", err)
	}
	realID, err := uuid.Parse(remote.ID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: invalid UUID %q: %w", remote.ID, err)
	}

	if !s.Entitled(ctx, realID.String()) {
		return uuid.Nil, ErrNotEntitled
	}

	now := time.Now()

	// Use Ent Upsert
	err = s.client.Tenant.Create().
		SetID(realID).
		SetName(remote.Name).
		SetSlug(remote.Slug).
		SetStatus(remote.Status).
		SetNillableUseCase(nillableStr(remote.UseCase)).
		SetSyncStatus("synced").
		SetLastSyncAt(now).
		OnConflictColumns(enttenant.FieldID).
		UpdateNewValues().
		Exec(ctx)

	if err != nil {
		return uuid.Nil, fmt.Errorf("tenant.Syncer: upsert failed: %w", err)
	}

	log.Printf("  [tenant-sync] dynamically synced %s (UUID %s) into logistics-api DB", slug, realID)
	return realID, nil
}

// nillableStr returns a *string if non-empty, nil otherwise.
func nillableStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
