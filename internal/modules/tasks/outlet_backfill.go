package tasks

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/bengobox/logistics-service/internal/ent/task"
)

// SetOutletIfMissing gives the tasks of one source reference (e.g. "order:<id>") the outlet the
// source names, only where the task has none yet. The source service owns the outlet (ordering
// owns an order's), so it calls this through the S2S route: on task creation gaps and in its
// startup backfill. Idempotent: a task that already has an outlet is never changed. Returns how
// many tasks were updated.
func (s *Service) SetOutletIfMissing(ctx context.Context, tenantID uuid.UUID, externalRef string, outletID uuid.UUID) (int, error) {
	externalRef = strings.TrimSpace(externalRef)
	if externalRef == "" || outletID == uuid.Nil {
		return 0, nil
	}
	return s.client.Task.Update().
		Where(task.TenantID(tenantID), task.ExternalReference(externalRef), task.OutletIDIsNil()).
		SetOutletID(outletID).
		Save(ctx)
}
