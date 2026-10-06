package tasks

import (
	"context"
	"fmt"
	sharedcache "github.com/Bengo-Hub/cache"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/task"
	notifmod "github.com/bengobox/logistics-service/internal/modules/notifications"
	"github.com/bengobox/logistics-service/internal/platform/events"
)

// SLAMonitor periodically scans for tasks whose SLA deadline has passed and
// publishes logistics.task.sla_breached events for downstream consumers
// (e.g. notifications-api, escalation workflows).
type SLAMonitor struct {
	log       *zap.Logger
	client    *ent.Client
	publisher *events.Publisher
	interval  time.Duration
	notifSvc  *notifmod.Service
}

// NewSLAMonitor creates a new SLAMonitor.
func NewSLAMonitor(log *zap.Logger, client *ent.Client, publisher *events.Publisher, interval time.Duration) *SLAMonitor {
	if interval == 0 {
		interval = 5 * time.Minute
	}
	return &SLAMonitor{
		log:       log.Named("tasks.sla_monitor"),
		client:    client,
		publisher: publisher,
		interval:  interval,
	}
}

// SetNotifications wires the dispatcher-alert service so each newly-detected breach also
// raises a notification (deduplicated so a still-breached task doesn't re-alert every tick).
func (m *SLAMonitor) SetNotifications(svc *notifmod.Service) { m.notifSvc = svc }

// Start runs the SLA breach check on a fixed interval until ctx is cancelled.
func (m *SLAMonitor) Start(ctx context.Context) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	m.log.Info("SLA monitor started", zap.Duration("interval", m.interval))

	for {
		select {
		case <-ctx.Done():
			m.log.Info("SLA monitor stopped")
			return
		case <-ticker.C:
			m.checkBreaches(ctx)
		}
	}
}

// maxBreachScan bounds one scan; the oldest breaches come first and the rest are picked up
// on later ticks.
const maxBreachScan = 1000

func (m *SLAMonitor) checkBreaches(ctx context.Context) {
	// Every replica runs this ticker; one replica per interval does the scan.
	if !sharedcache.ClaimPeriod(ctx, "logistics:sla-monitor", m.interval) {
		return
	}
	now := time.Now().UTC()

	// Open tasks whose SLA due time has passed (served by the task_sla_open partial index).
	overdue, err := m.client.Task.Query().
		Where(
			task.SLADueAtLTE(now),
			task.StatusNotIn(TerminalStatuses...),
		).
		Order(ent.Asc(task.FieldSLADueAt)).
		Limit(maxBreachScan).
		All(ctx)
	if err != nil {
		m.log.Error("SLA monitor: query failed", zap.Error(err))
		return
	}

	if len(overdue) == 0 {
		return
	}

	m.log.Warn("SLA breaches detected", zap.Int("count", len(overdue)))

	for _, t := range overdue {
		breachAge := now.Sub(*t.SLADueAt)

		// Act once per escalation step per task (breached, warning, critical, escalated). The
		// scan repeats every interval for as long as the task stays open; before, every scan
		// re-published sla_breached for every overdue task, flooding consumers.
		level := EscalationLevel(t)
		if level == "" {
			level = "breached"
		}
		if !sharedcache.ClaimOnce(ctx, "logistics:sla:"+t.ID.String()+":"+level, 30*24*time.Hour) {
			continue
		}

		m.log.Warn("task SLA breached",
			zap.String("task_id", t.ID.String()),
			zap.String("tenant_id", t.TenantID.String()),
			zap.String("status", t.Status),
			zap.Duration("breach_age", breachAge),
		)

		if m.notifSvc != nil {
			taskID := t.ID
			label := t.TrackingCode
			if label == "" {
				label = t.ExternalReference
			}
			if label == "" {
				label = taskID.String()[:8]
			}
			_, notifErr := m.notifSvc.CreateDeduped(ctx, notifmod.CreateRequest{
				TenantID:         t.TenantID,
				NotificationType: "sla_breach",
				Title:            fmt.Sprintf("SLA breached: %s", label),
				Body:             fmt.Sprintf("Task %s is %s overdue and still %s.", label, breachAge.Round(time.Minute), t.Status),
				Payload: map[string]any{
					"task_id":     taskID.String(),
					"status":      t.Status,
					"breach_mins": int(breachAge.Minutes()),
				},
				RelatedTaskID: &taskID,
			})
			if notifErr != nil {
				m.log.Error("failed to create SLA breach notification",
					zap.String("task_id", t.ID.String()),
					zap.Error(notifErr))
			}
		}

		if m.publisher == nil {
			continue
		}

		data := events.TaskEventData{
			TaskID:            t.ID.String(),
			TrackingCode:      t.TrackingCode,
			ExternalReference: t.ExternalReference,
			Status:            t.Status,
		}
		if err := m.publisher.PublishTaskSLABreached(ctx, t.TenantID, data); err != nil {
			m.log.Error("failed to publish SLA breach event",
				zap.String("task_id", t.ID.String()),
				zap.Error(err))
		}
	}
}

// escalationThresholds maps how long past SLA before escalating to the next level.
// These trigger re-dispatch or supervisor alerts via the notifications-api consumer.
var escalationThresholds = []struct {
	After time.Duration
	Level string
}{
	{15 * time.Minute, "warning"},
	{30 * time.Minute, "critical"},
	{60 * time.Minute, "escalated"},
}

// EscalationLevel returns the current escalation level for a task based on how
// long it has been overdue. Returns "" if the task is not yet overdue.
func EscalationLevel(t *ent.Task) string {
	if t.SLADueAt == nil {
		return ""
	}
	breach := time.Since(*t.SLADueAt)
	if breach <= 0 {
		return ""
	}
	level := ""
	for _, threshold := range escalationThresholds {
		if breach >= threshold.After {
			level = threshold.Level
		}
	}
	return level
}

// GetOverdueTasks returns all non-terminal tasks past their SLA deadline for a tenant.
func GetOverdueTasks(ctx context.Context, client *ent.Client, tenantID uuid.UUID) ([]*ent.Task, error) {
	return client.Task.Query().
		Where(
			task.TenantID(tenantID),
			task.SLADueAtLTE(time.Now().UTC()),
			task.StatusNotIn(TerminalStatuses...),
		).
		Order(ent.Asc(task.FieldSLADueAt)).
		All(ctx)
}
