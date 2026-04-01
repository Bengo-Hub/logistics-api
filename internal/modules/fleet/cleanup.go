package fleet

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent/fleetmember"
	"github.com/bengobox/logistics-service/internal/platform/events"
)

const (
	// StaleRiderAge is the maximum age of a pending rider before auto-cleanup.
	StaleRiderAge = 7 * 24 * time.Hour
	// CleanupInterval is how often the cleanup job runs.
	CleanupInterval = 1 * time.Hour
)

// StartStaleRiderCleanup runs a background job that deletes rider applications
// that have not been reviewed within StaleRiderAge (7 days).
// Sends a fleet.member_expired notification to both rider and tenant admin.
func (s *Service) StartStaleRiderCleanup(ctx context.Context) {
	s.log.Info("stale rider cleanup job started",
		zap.Duration("check_interval", CleanupInterval),
		zap.Duration("stale_after", StaleRiderAge),
	)

	ticker := time.NewTicker(CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.log.Info("stale rider cleanup job stopped")
			return
		case <-ticker.C:
			s.cleanupStaleRiders(ctx)
		}
	}
}

func (s *Service) cleanupStaleRiders(ctx context.Context) {
	cutoff := time.Now().Add(-StaleRiderAge)

	stale, err := s.client.FleetMember.Query().
		Where(
			fleetmember.StatusIn("invited", "pending", "pending_review"),
			fleetmember.CreatedAtLT(cutoff),
		).
		WithUser().
		All(ctx)
	if err != nil {
		s.log.Error("cleanup: query stale riders", zap.Error(err))
		return
	}

	if len(stale) == 0 {
		return
	}

	s.log.Info("cleanup: found stale rider applications", zap.Int("count", len(stale)))

	for _, m := range stale {
		email := ""
		name := ""
		if u := m.Edges.User; u != nil {
			email = u.Email
			name = u.FullName
		}

		// Publish expired event before deletion
		if s.publisher != nil && email != "" {
			tenantSlug := s.resolveTenantSlug(ctx, m.FleetID)
			if pubErr := s.publisher.PublishFleetMemberExpired(ctx, m.TenantID, events.FleetMemberEventData{
				MemberID:   m.ID.String(), UserID: m.UserID.String(),
				FleetID:    m.FleetID.String(), UserEmail: email, UserName: name,
				TenantSlug: tenantSlug,
			}); pubErr != nil {
				s.log.Warn("cleanup: failed to publish expired event", zap.Error(pubErr))
			}
		}

		// Delete vehicle if exists
		if m.VehicleID != nil {
			_ = s.client.Vehicle.DeleteOneID(*m.VehicleID).Exec(ctx)
		}

		// Delete fleet member
		if err := s.client.FleetMember.DeleteOneID(m.ID).Exec(ctx); err != nil {
			s.log.Error("cleanup: delete stale member", zap.String("member_id", m.ID.String()), zap.Error(err))
			continue
		}

		s.log.Info("cleanup: removed stale rider application",
			zap.String("member_id", m.ID.String()),
			zap.String("email", email),
			zap.String("status", m.Status),
		)
	}
}
