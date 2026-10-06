package telemetry

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	sharedcache "github.com/Bengo-Hub/cache"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent/serviceconfig"
)

// Location history retention. Every rider sends a fix every 15 seconds while on a job, about
// 5,700 rows per rider per day, and nothing ever removed them. The fleet map and ETA only need
// the latest fix (Redis); the history is kept for disputes and route review for a configurable
// number of days (logistics.telemetry_retention_days, default 30) and pruned daily in batches.

const (
	retentionConfigKey     = "logistics.telemetry_retention_days"
	defaultRetentionDays   = 30
	retentionBatch         = 5000
	staleStreamAfter       = 12 * time.Hour
	retentionCheckInterval = time.Hour
)

// retentionCutoff returns the oldest capture time to keep for a retention of days (minimum 1).
func retentionCutoff(now time.Time, days int) time.Time {
	if days < 1 {
		days = defaultRetentionDays
	}
	return now.UTC().AddDate(0, 0, -days)
}

// parseRetentionDays reads the configured value, falling back to the default.
func parseRetentionDays(v string) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 1 {
		return defaultRetentionDays
	}
	return n
}

func (s *Service) retentionDays(ctx context.Context) int {
	cfg, err := s.client.ServiceConfig.Query().
		Where(serviceconfig.ConfigKey(retentionConfigKey), serviceconfig.TenantIDIsNil()).
		First(ctx)
	if err != nil {
		return defaultRetentionDays
	}
	return parseRetentionDays(cfg.ConfigValue)
}

// StartRetentionJob prunes old location points once per UTC day fleet-wide (checked hourly, so a
// deploy never skips a day) and closes streams that stopped reporting.
func (s *Service) StartRetentionJob(ctx context.Context, db *sql.DB) {
	if db == nil {
		return
	}
	run := func() {
		if !sharedcache.ClaimPeriod(ctx, "logistics:telemetry-retention", 24*time.Hour) {
			return
		}
		s.prune(ctx, db)
	}
	ticker := time.NewTicker(retentionCheckInterval)
	defer ticker.Stop()
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Service) prune(ctx context.Context, db *sql.DB) {
	cutoff := retentionCutoff(time.Now(), s.retentionDays(ctx))
	total := int64(0)
	for {
		// Bounded batches keep each delete short (no long lock, no huge WAL burst).
		res, err := db.ExecContext(ctx, `
			DELETE FROM telemetry_points
			WHERE id IN (SELECT id FROM telemetry_points WHERE captured_at < $1 LIMIT $2)`,
			cutoff, retentionBatch)
		if err != nil {
			s.log.Warn("telemetry retention: delete failed", zap.Error(err))
			break
		}
		n, _ := res.RowsAffected()
		total += n
		if n < retentionBatch || ctx.Err() != nil {
			break
		}
	}
	// A rider who closed the app without ending the stream leaves it "active" forever.
	ended, err := db.ExecContext(ctx, `
		UPDATE telemetry_streams s SET status = 'ended', ended_at = now()
		WHERE s.status = 'active' AND s.started_at < $1
		  AND NOT EXISTS (SELECT 1 FROM telemetry_points p WHERE p.stream_id = s.id AND p.captured_at >= $1)`,
		time.Now().UTC().Add(-staleStreamAfter))
	var closed int64
	if err == nil {
		closed, _ = ended.RowsAffected()
	}
	_, _ = db.ExecContext(ctx, `
		DELETE FROM telemetry_streams s
		WHERE s.status = 'ended' AND s.started_at < $1
		  AND NOT EXISTS (SELECT 1 FROM telemetry_points p WHERE p.stream_id = s.id)`, cutoff)
	s.log.Info("telemetry retention done",
		zap.Time("cutoff", cutoff), zap.Int64("points_deleted", total), zap.Int64("streams_closed", closed))
}
