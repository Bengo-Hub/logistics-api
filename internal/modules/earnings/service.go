package earnings

import (
	"context"
	"database/sql"

	entsql "entgo.io/ent/dialect/sql"
	"fmt"
	sharedcache "github.com/Bengo-Hub/cache"
	"github.com/bengobox/logistics-service/internal/ent/earningsstatement"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/billingevent"
	"github.com/bengobox/logistics-service/internal/ent/predicate"
)

// Service handles earnings calculation and billing event recording.
type Service struct {
	client *ent.Client
	db     *sql.DB // for SQL aggregation (ent here has no raw-query feature)
	log    *zap.Logger
	// fallbackPricer prices a distance when the tenant has no rider pricing rule.
	fallbackPricer func(ctx context.Context, tenantID uuid.UUID, distanceKm float64) (float64, error)
}

// WithDB sets the database handle used for statement aggregation.
func (s *Service) WithDB(db *sql.DB) *Service {
	s.db = db
	return s
}

// NewService creates a new earnings service.
// SetFallbackPricer sets the per-distance price used when a tenant has no active rider
// pricing rule. app.go wires the zones delivery policy here.
func (s *Service) SetFallbackPricer(f func(ctx context.Context, tenantID uuid.UUID, distanceKm float64) (float64, error)) {
	s.fallbackPricer = f
}

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
		// No rider pricing rule: pay what the tenant's delivery policy would charge for
		// this distance, so every tenant setting lives in one place (logistics-ui).
		if s.fallbackPricer == nil {
			return fmt.Errorf("earnings: no pricing rule and no fallback pricer: %w", err)
		}
		fee, err = s.fallbackPricer(ctx, tenantID, distanceKm)
		if err != nil {
			return fmt.Errorf("earnings: fallback pricing: %w", err)
		}
		fee = roundCents(fee)
	}

	// Create billing event (fleet_member_id stored in metadata since schema uses generic metadata JSON)
	if err := s.createEarningEvent(ctx, tenantID, taskID, memberID, fee, map[string]any{
		"distance_km": distanceKm,
		"description": fmt.Sprintf("Delivery earning for task (%.1f km)", distanceKm),
	}); err != nil {
		return err
	}

	s.log.Info("delivery earning recorded",
		zap.String("task_id", taskID.String()),
		zap.String("member_id", memberID.String()),
		zap.Float64("amount", fee),
		zap.Float64("distance_km", distanceKm),
	)

	return nil
}

// RecordEarningWithAmount records a delivery earning using an explicit, known
// amount (e.g. the order's actual delivery fee) instead of recomputing the fee
// from distance and pricing rules. Used when ordering-backend propagates the
// real delivery_fee onto the task metadata.
func (s *Service) RecordEarningWithAmount(ctx context.Context, tenantID, taskID, memberID uuid.UUID, amount float64) error {
	fee := roundCents(amount)
	if err := s.createEarningEvent(ctx, tenantID, taskID, memberID, fee, map[string]any{
		"source":      "delivery_fee",
		"description": "Delivery earning for task (order delivery fee)",
	}); err != nil {
		return err
	}

	s.log.Info("delivery earning recorded",
		zap.String("task_id", taskID.String()),
		zap.String("member_id", memberID.String()),
		zap.Float64("amount", fee),
		zap.String("source", "delivery_fee"),
	)

	return nil
}

// createEarningEvent persists a delivery_earning BillingEvent. The fleet member
// id is always stored in metadata under "fleet_member_id" (the schema uses a
// generic metadata JSON), since GetMyEarnings resolves riders by that key.
func (s *Service) createEarningEvent(ctx context.Context, tenantID, taskID, memberID uuid.UUID, fee float64, extra map[string]any) error {
	meta := map[string]any{
		"fleet_member_id": memberID.String(),
	}
	for k, v := range extra {
		meta[k] = v
	}

	_, err := s.client.BillingEvent.Create().
		SetTenantID(tenantID).
		SetTaskID(taskID).
		SetEventType("delivery_earning").
		SetAmount(fee).
		SetCurrency("KES").
		SetOccurredAt(time.Now()).
		SetMetadata(meta).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("earnings: create billing event: %w", err)
	}
	return nil
}

// GenerateStatements writes one draft earnings statement per fleet member for the UTC day
// [periodStart, periodStart+24h), from that day's delivery_earning billing events.
//
// Fixes over the previous version: it summed EVERY delivery_earning event the tenant ever
// had and saved the cumulative total as "yesterday", so each day's statement repeated and
// grew; it loaded all rows into memory to sum in Go; and a re-run (restart, second replica)
// wrote duplicate statements. Now the sum is a SQL GROUP BY over the bounded day (using the
// tenant/type/occurred_at filter), and a member that already has a statement for the day is
// skipped.
func (s *Service) GenerateStatements(ctx context.Context, tenantID uuid.UUID, periodStart time.Time) error {
	periodStart = periodStart.UTC().Truncate(24 * time.Hour)
	periodEnd := periodStart.Add(24 * time.Hour)

	totals, err := s.memberTotals(ctx, tenantID, periodStart, periodEnd)
	if err != nil {
		return fmt.Errorf("earnings: aggregate events: %w", err)
	}
	for memberID, grossAmount := range totals {
		exists, err := s.client.EarningsStatement.Query().
			Where(
				earningsstatement.TenantID(tenantID),
				earningsstatement.FleetMemberID(memberID),
				earningsstatement.PeriodStart(periodStart),
			).
			Exist(ctx)
		if err != nil {
			return fmt.Errorf("earnings: check existing statement: %w", err)
		}
		if exists {
			continue
		}
		_, err = s.client.EarningsStatement.Create().
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
			zap.Time("period_start", periodStart),
			zap.Float64("gross_amount", grossAmount),
		)
	}
	return nil
}

// memberTotals sums the day's delivery earnings per fleet member in SQL.
func (s *Service) memberTotals(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (map[uuid.UUID]float64, error) {
	out := map[uuid.UUID]float64{}
	if s.db == nil {
		return out, fmt.Errorf("earnings: database handle not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT metadata->>'fleet_member_id' AS member, SUM(amount)
		FROM billing_events
		WHERE tenant_id = $1 AND event_type = 'delivery_earning'
		  AND occurred_at >= $2 AND occurred_at < $3
		  AND metadata ? 'fleet_member_id'
		GROUP BY 1`, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var member string
		var sum float64
		if err := rows.Scan(&member, &sum); err != nil {
			return nil, err
		}
		if id, err := uuid.Parse(member); err == nil {
			out[id] = roundCents(sum)
		}
	}
	return out, rows.Err()
}

// StartStatementJob generates yesterday's statements once per UTC day, fleet-wide. It checks
// hourly (and at start) rather than on a 24h ticker: a 24h ticker restarts with every deploy,
// so with deploys more often than daily it never fired. ClaimPeriod gives one run per day
// across all replicas; GenerateStatements skips statements that already exist.
func (s *Service) StartStatementJob(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	s.log.Info("earnings statement job started (daily, checked hourly)")

	run := func() {
		if !sharedcache.ClaimPeriod(ctx, "logistics:earnings-statements", 24*time.Hour) {
			return
		}
		tenantIDs, err := s.getDistinctBillingTenantIDs(ctx)
		if err != nil {
			s.log.Error("failed to get billing tenant IDs", zap.Error(err))
			return
		}
		yesterday := time.Now().UTC().Add(-24 * time.Hour)
		for _, tid := range tenantIDs {
			if err := s.GenerateStatements(ctx, tid, yesterday); err != nil {
				s.log.Error("failed to generate statements for tenant",
					zap.String("tenant_id", tid.String()), zap.Error(err))
			}
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("earnings statement job stopped")
			return
		case <-ticker.C:
			run()
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

// memberMeta is the metadata containment value that selects one rider's events. It matches the
// GIN (jsonb_path_ops) index on billing_events.metadata.
func memberMeta(memberID uuid.UUID) string {
	return `{"fleet_member_id":"` + memberID.String() + `"}`
}

// ForMember restricts a billing event query to one rider's events, in SQL. The rider screens
// used to fetch the latest 200 events of the whole tenant and filter in Go, so a rider in a busy
// fleet saw an empty or partial history.
func ForMember(memberID uuid.UUID) predicate.BillingEvent {
	val := memberMeta(memberID)
	return predicate.BillingEvent(func(s *entsql.Selector) {
		s.Where(entsql.P(func(b *entsql.Builder) {
			b.WriteString(s.C(billingevent.FieldMetadata))
			b.WriteString(" @> ")
			b.Arg(val)
			b.WriteString("::jsonb")
		}))
	})
}

// RiderTotals returns a rider's delivery earnings since each of the given times, in one query.
func (s *Service) RiderTotals(ctx context.Context, tenantID, memberID uuid.UUID, today, week, month time.Time) (float64, float64, float64, error) {
	if s.db == nil {
		return 0, 0, 0, fmt.Errorf("earnings: database handle not configured")
	}
	earliest := month
	for _, t := range []time.Time{today, week} {
		if t.Before(earliest) {
			earliest = t
		}
	}
	var d, w, m float64
	err := s.db.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(amount) FILTER (WHERE occurred_at >= $3), 0),
		  COALESCE(SUM(amount) FILTER (WHERE occurred_at >= $4), 0),
		  COALESCE(SUM(amount) FILTER (WHERE occurred_at >= $5), 0)
		FROM billing_events
		WHERE tenant_id = $1 AND event_type = 'delivery_earning'
		  AND metadata @> $2::jsonb AND occurred_at >= $6`,
		tenantID, memberMeta(memberID), today, week, month, earliest).Scan(&d, &w, &m)
	return roundCents(d), roundCents(w), roundCents(m), err
}
