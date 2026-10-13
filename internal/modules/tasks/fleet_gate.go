package tasks

import (
	"context"
	"errors"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
)

// Tasks from other services (online orders, POS till deliveries, inventory transfers, any
// S2S caller) are only accepted for a tenant that runs a fleet: at least one active rider,
// staff or freelance. A business that delivers off the books (no riders in logistics) would
// otherwise pile up jobs nobody can take. This is the one check; every intake path goes
// through CreateTask or CreateTaskFromOrder.

// ErrNotEntitled means the tenant's subscription has no logistics, so integration tasks are
// refused.
var ErrNotEntitled = errors.New("tasks: logistics is not part of this business's subscription")

// SetEntitlementGate wires the subscription check (does the plan include logistics).
func (s *Service) SetEntitlementGate(fn func(ctx context.Context, tenantID string) bool) {
	s.entitled = fn
}

// SetFeatureGate wires the subscription feature check used by background paths that have no
// user token (auto-assign). It fails open when subscriptions-api cannot be reached.
func (s *Service) SetFeatureGate(fn func(ctx context.Context, tenantID, feature string) bool) {
	s.featured = fn
}

// intakeAllowed is the one check for tasks from other services: the plan includes logistics
// and the tenant runs at least one active rider.
func (s *Service) intakeAllowed(ctx context.Context, tenantID uuid.UUID) error {
	if s.entitled != nil && !s.entitled(ctx, tenantID.String()) {
		return ErrNotEntitled
	}
	if !s.HasActiveFleet(ctx, tenantID) {
		return ErrNoActiveFleet
	}
	return nil
}

// ErrNoActiveFleet means the tenant has no active rider, so integration tasks are refused.
var ErrNoActiveFleet = errors.New("tasks: this business has no active riders in logistics; add riders before sending deliveries")

// fleetGateTTL bounds how long a "no fleet" answer is cached; activating a rider drops it.
const fleetGateTTL = 2 * time.Minute

func fleetGateKey(tenantID uuid.UUID) string {
	return sharedcache.Key("log", "fleet-active", tenantID.String())
}

// SetCache wires the shared cache used for the fleet gate. Optional.
func (s *Service) SetCache(c *sharedcache.Aside) { s.cache = c }

// HasActiveFleet reports whether the tenant has at least one active rider.
func (s *Service) HasActiveFleet(ctx context.Context, tenantID uuid.UUID) bool {
	check := func(ctx context.Context) (bool, error) {
		return s.client.FleetMember.Query().
			Where(fleetmember.TenantID(tenantID), fleetmember.Status("active")).
			Exist(ctx)
	}
	if s.cache == nil {
		ok, err := check(ctx)
		return err != nil || ok // fail open on a database error: never drop a delivery on a hiccup
	}
	ok, err := sharedcache.GetOrSet(ctx, s.cache, fleetGateKey(tenantID), fleetGateTTL, check)
	return err != nil || ok
}

// InvalidateFleetGate drops the cached answer after a rider is activated, suspended or removed.
func (s *Service) InvalidateFleetGate(ctx context.Context, tenantID uuid.UUID) {
	if s.cache != nil {
		s.cache.Invalidate(ctx, fleetGateKey(tenantID))
	}
}

// isIntegrationSource reports whether a task comes from another service rather than a
// dispatcher in logistics-ui.
func isIntegrationSource(source string) bool {
	return source != "" && source != "logistics"
}
