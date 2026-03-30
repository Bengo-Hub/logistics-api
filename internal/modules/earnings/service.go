package earnings

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/billingevent"
)

// Service handles earnings calculation and billing event recording.
type Service struct {
	client *ent.Client
	log    *zap.Logger
}

// NewService creates a new earnings service.
func NewService(client *ent.Client, log *zap.Logger) *Service {
	return &Service{
		client: client,
		log:    log.Named("earnings.service"),
	}
}

// RecordEarning calculates the rider earning for a completed delivery and creates a BillingEvent.
func (s *Service) RecordEarning(ctx context.Context, tenantID, taskID, memberID uuid.UUID, distanceKm float64) error {
	// Calculate the delivery fee using pricing rules
	fee, err := CalculateDeliveryFee(ctx, s.client, tenantID, distanceKm)
	if err != nil {
		s.log.Warn("could not calculate delivery fee, using distance-based fallback",
			zap.Error(err),
			zap.Float64("distance_km", distanceKm))
		// Fallback: simple per-km rate
		fee = roundCents(50.0 + (distanceKm * 20.0)) // base 50 + 20/km
	}

	// Create billing event (fleet_member_id stored in metadata since schema uses generic metadata JSON)
	_, err = s.client.BillingEvent.Create().
		SetTenantID(tenantID).
		SetTaskID(taskID).
		SetEventType("delivery_earning").
		SetAmount(fee).
		SetCurrency("KES").
		SetOccurredAt(time.Now()).
		SetMetadata(map[string]any{
			"fleet_member_id": memberID.String(),
			"distance_km":    distanceKm,
			"description":    fmt.Sprintf("Delivery earning for task (%.1f km)", distanceKm),
		}).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("earnings: create billing event: %w", err)
	}

	s.log.Info("delivery earning recorded",
		zap.String("task_id", taskID.String()),
		zap.String("member_id", memberID.String()),
		zap.Float64("amount", fee),
		zap.Float64("distance_km", distanceKm),
	)

	return nil
}

// GenerateStatements aggregates delivery earning billing events into earnings statements.
// This should be called periodically (e.g., daily cron).
func (s *Service) GenerateStatements(ctx context.Context, tenantID uuid.UUID) error {
	// Get all delivery_earning billing events for this tenant
	events, err := s.client.BillingEvent.Query().
		Where(
			billingevent.TenantID(tenantID),
			billingevent.EventType("delivery_earning"),
		).
		All(ctx)
	if err != nil {
		return fmt.Errorf("earnings: query events: %w", err)
	}

	if len(events) == 0 {
		return nil
	}

	// Group by fleet member (extracted from metadata)
	grouped := make(map[uuid.UUID]float64)
	for _, evt := range events {
		memberIDStr, _ := evt.Metadata["fleet_member_id"].(string)
		if memberIDStr == "" {
			continue
		}
		memberID, parseErr := uuid.Parse(memberIDStr)
		if parseErr != nil {
			continue
		}
		grouped[memberID] += evt.Amount
	}

	now := time.Now()
	periodStart := now.AddDate(0, 0, -1).Truncate(24 * time.Hour)
	periodEnd := now.Truncate(24 * time.Hour)

	for memberID, grossAmount := range grouped {
		_, err := s.client.EarningsStatement.Create().
			SetTenantID(tenantID).
			SetFleetMemberID(memberID).
			SetPeriodStart(periodStart).
			SetPeriodEnd(periodEnd).
			SetGrossAmount(grossAmount).
			SetNetAmount(grossAmount).
			SetBonusAmount(0).
			SetDeductionAmount(0).
			SetStatus("draft").
			Save(ctx)
		if err != nil {
			s.log.Error("failed to create earnings statement",
				zap.Error(err),
				zap.String("member_id", memberID.String()))
			continue
		}

		s.log.Info("earnings statement generated",
			zap.String("member_id", memberID.String()),
			zap.Float64("gross_amount", grossAmount),
		)
	}

	return nil
}

// StartStatementJob runs the statement generation job on a daily schedule.
func (s *Service) StartStatementJob(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	s.log.Info("earnings statement job started (daily)")

	for {
		select {
		case <-ctx.Done():
			s.log.Info("earnings statement job stopped")
			return
		case <-ticker.C:
			// Process all tenants — query distinct tenant IDs from billing events
			tenantIDs, err := s.getDistinctBillingTenantIDs(ctx)
			if err != nil {
				s.log.Error("failed to get billing tenant IDs", zap.Error(err))
				continue
			}
			for _, tid := range tenantIDs {
				if err := s.GenerateStatements(ctx, tid); err != nil {
					s.log.Error("failed to generate statements for tenant",
						zap.String("tenant_id", tid.String()),
						zap.Error(err))
				}
			}
		}
	}
}

func (s *Service) getDistinctBillingTenantIDs(ctx context.Context) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := s.client.BillingEvent.Query().
		Where(billingevent.EventType("delivery_earning")).
		GroupBy(billingevent.FieldTenantID).
		Scan(ctx, &ids)
	return ids, err
}
