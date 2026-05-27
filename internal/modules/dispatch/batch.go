package dispatch

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/tenant"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

const (
	// defaultBatchRadius is the max distance (km) between pickup points to form a batch.
	defaultBatchRadius = 0.5
	// maxBatchSize limits how many tasks can be batched to a single rider.
	maxBatchSize = 5
)

// locatedTask pairs a task with its extracted pickup coordinates.
type locatedTask struct {
	task *ent.Task
	lat  float64
	lng  float64
}

// BatchScheduler periodically groups nearby pending tasks and assigns them to
// the nearest available rider. It complements the single-task DispatchTask flow
// by aggregating orders whose pickup points are within a configurable radius.
type BatchScheduler struct {
	log        *zap.Logger
	entClient  *ent.Client
	dispatcher *AutoDispatcher
	interval   time.Duration
}

// NewBatchScheduler creates a new periodic batch dispatcher.
func NewBatchScheduler(
	log *zap.Logger,
	entClient *ent.Client,
	dispatcher *AutoDispatcher,
	interval time.Duration,
) *BatchScheduler {
	if interval == 0 {
		interval = 2 * time.Minute
	}
	return &BatchScheduler{
		log:        log.Named("dispatch.batch"),
		entClient:  entClient,
		dispatcher: dispatcher,
		interval:   interval,
	}
}

// Start begins the periodic batch dispatch loop. It blocks until ctx is cancelled.
func (s *BatchScheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.log.Info("batch scheduler started", zap.Duration("interval", s.interval))

	for {
		select {
		case <-ctx.Done():
			s.log.Info("batch scheduler stopped")
			return
		case <-ticker.C:
			s.runBatchCycle(ctx)
		}
	}
}

// runBatchCycle processes all tenants that have pending tasks.
// Tenants are processed in pages of 100 to avoid loading the full tenant list into memory.
func (s *BatchScheduler) runBatchCycle(ctx context.Context) {
	const pageSize = 100
	offset := 0
	for {
		tenants, err := s.entClient.Tenant.Query().
			Where(tenant.StatusEQ("active")).
			Limit(pageSize).
			Offset(offset).
			All(ctx)
		if err != nil {
			s.log.Warn("batch: failed to query tenants", zap.Error(err))
			return
		}
		if len(tenants) == 0 {
			break
		}
		for _, t := range tenants {
			if err := s.batchDispatch(ctx, t.ID); err != nil {
				s.log.Warn("batch dispatch failed for tenant",
					zap.String("tenant_id", t.ID.String()),
					zap.Error(err),
				)
			}
		}
		if len(tenants) < pageSize {
			break
		}
		offset += pageSize
	}
}

// batchDispatch groups nearby pending tasks for a tenant and assigns each group
// to the nearest available rider.
func (s *BatchScheduler) batchDispatch(ctx context.Context, tenantID uuid.UUID) error {
	// 1. Query unassigned pending tasks with steps loaded
	pendingTasks, err := s.entClient.Task.Query().
		Where(
			task.TenantID(tenantID),
			task.Status("pending"),
		).
		WithSteps().
		Order(ent.Asc(task.FieldCreatedAt)).
		Limit(100).
		All(ctx)
	if err != nil {
		return fmt.Errorf("batch: query pending tasks: %w", err)
	}

	if len(pendingTasks) < 2 {
		// Not enough tasks to form a batch; single tasks are handled by DispatchTask
		return nil
	}

	// 2. Filter tasks that have a pickup location
	var located []locatedTask
	for _, t := range pendingTasks {
		lat, lng, ok := extractPickupLocation(t)
		if !ok {
			continue
		}
		located = append(located, locatedTask{task: t, lat: lat, lng: lng})
	}

	if len(located) < 2 {
		return nil
	}

	// 3. Group by proximity using greedy clustering
	groups := groupByProximity(located, defaultBatchRadius)

	s.log.Debug("batch: formed groups",
		zap.String("tenant_id", tenantID.String()),
		zap.Int("pending", len(located)),
		zap.Int("groups", len(groups)),
	)

	// 4. For each group with 2+ tasks, find nearest rider and assign all
	for _, group := range groups {
		if len(group) < 2 {
			continue // single-task groups are handled by the normal dispatch flow
		}

		// Use the first task's pickup as the reference point for rider search
		refLat := group[0].lat
		refLng := group[0].lng

		// Get active fleet members
		members, err := s.dispatcher.fleetSvc.ListMembers(ctx, tenantID, "active")
		if err != nil || len(members) == 0 {
			continue
		}

		// Find nearest riders
		candidates, err := s.dispatcher.findNearestRiders(ctx, tenantID, members, refLat, refLng)
		if err != nil {
			candidates = s.dispatcher.fallbackHaversine(ctx, tenantID, members, refLat, refLng)
		}

		if len(candidates) == 0 {
			continue
		}

		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].DistanceKm < candidates[j].DistanceKm
		})

		// Filter by max dispatch radius
		var eligible []riderCandidate
		for _, c := range candidates {
			if c.DistanceKm <= maxDispatchRadiusKm {
				eligible = append(eligible, c)
			}
		}

		if len(eligible) == 0 {
			continue
		}

		rider := eligible[0]

		// Cap the batch size
		batchSize := len(group)
		if batchSize > maxBatchSize {
			batchSize = maxBatchSize
		}

		// Assign each task in the group to the same rider
		assignedCount := 0
		for i := 0; i < batchSize; i++ {
			_, assignErr := s.dispatcher.taskSvc.AssignTask(ctx, tenantID, group[i].task.ID, tasks.AssignTaskRequest{
				FleetMemberID: rider.MemberID,
			})
			if assignErr != nil {
				s.log.Debug("batch: assign task failed",
					zap.String("task_id", group[i].task.ID.String()),
					zap.Error(assignErr),
				)
				continue
			}
			assignedCount++
		}

		if assignedCount > 0 {
			s.log.Info("batch dispatched tasks to rider",
				zap.String("rider_id", rider.MemberID.String()),
				zap.Int("count", assignedCount),
				zap.Float64("distance_km", rider.DistanceKm),
			)
		}
	}

	return nil
}

// groupByProximity clusters located tasks by pickup proximity using a greedy
// single-linkage approach: each unvisited task is added to the current cluster
// if it is within radiusKm of any task already in that cluster.
func groupByProximity(located []locatedTask, radiusKm float64) [][]locatedTask {
	visited := make([]bool, len(located))
	var groups [][]locatedTask

	for i := range located {
		if visited[i] {
			continue
		}
		visited[i] = true
		group := []locatedTask{located[i]}

		// Expand cluster
		for j := i + 1; j < len(located); j++ {
			if visited[j] {
				continue
			}
			// Check if task j is within radius of any task already in the group
			for _, member := range group {
				dist := haversineKm(member.lat, member.lng, located[j].lat, located[j].lng)
				if dist <= radiusKm {
					visited[j] = true
					group = append(group, located[j])
					break
				}
			}
		}

		groups = append(groups, group)
	}

	return groups
}
