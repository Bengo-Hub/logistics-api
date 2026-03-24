package dispatch

import (
	"context"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	"github.com/bengobox/logistics-service/internal/modules/routing"
	"github.com/bengobox/logistics-service/internal/platform/events"
)

// ETAUpdater periodically recalculates and publishes ETA for in-progress deliveries.
type ETAUpdater struct {
	log        *zap.Logger
	entClient  *ent.Client
	routingSvc *routing.Service
	dispatcher *AutoDispatcher
	publisher  *events.Publisher
	interval   time.Duration
}

// NewETAUpdater creates a new periodic ETA updater.
func NewETAUpdater(
	log *zap.Logger,
	entClient *ent.Client,
	routingSvc *routing.Service,
	dispatcher *AutoDispatcher,
	publisher *events.Publisher,
	interval time.Duration,
) *ETAUpdater {
	if interval == 0 {
		interval = 30 * time.Second
	}
	return &ETAUpdater{
		log:        log.Named("dispatch.eta"),
		entClient:  entClient,
		routingSvc: routingSvc,
		dispatcher: dispatcher,
		publisher:  publisher,
		interval:   interval,
	}
}

// Start begins the periodic ETA update loop. It blocks until ctx is cancelled.
func (u *ETAUpdater) Start(ctx context.Context) {
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()

	u.log.Info("ETA updater started", zap.Duration("interval", u.interval))

	for {
		select {
		case <-ctx.Done():
			u.log.Info("ETA updater stopped")
			return
		case <-ticker.C:
			u.updateActiveETAs(ctx)
		}
	}
}

// ComputeAndPublishETA calculates ETA for a single task and publishes the event.
// Called on status transitions (accepted, en_route).
func (u *ETAUpdater) ComputeAndPublishETA(ctx context.Context, tenantID, taskID uuid.UUID) {
	t, err := u.entClient.Task.Query().
		Where(task.ID(taskID), task.TenantID(tenantID)).
		WithSteps().
		WithAssignments(func(q *ent.TaskAssignmentQuery) {
			q.Where(taskassignment.StatusIn("assigned", "accepted")).
				Limit(1)
		}).
		Only(ctx)
	if err != nil {
		u.log.Debug("eta: could not load task", zap.Error(err))
		return
	}

	u.computeETAForTask(ctx, t)
}

// updateActiveETAs recalculates ETA for all in-progress tasks.
func (u *ETAUpdater) updateActiveETAs(ctx context.Context) {
	// Find all tasks with status accepted or en_route
	activeTasks, err := u.entClient.Task.Query().
		Where(task.StatusIn("accepted", "en_route")).
		WithSteps().
		WithAssignments(func(q *ent.TaskAssignmentQuery) {
			q.Where(taskassignment.StatusIn("assigned", "accepted")).
				Limit(1)
		}).
		Limit(100).
		All(ctx)
	if err != nil {
		u.log.Warn("eta: failed to query active tasks", zap.Error(err))
		return
	}

	if len(activeTasks) == 0 {
		return
	}

	u.log.Debug("updating ETAs for active tasks", zap.Int("count", len(activeTasks)))

	for _, t := range activeTasks {
		u.computeETAForTask(ctx, t)
	}
}

// computeETAForTask calculates and publishes ETA for a single task.
func (u *ETAUpdater) computeETAForTask(ctx context.Context, t *ent.Task) {
	// Get assigned rider
	if len(t.Edges.Assignments) == 0 {
		return
	}
	assignment := t.Edges.Assignments[0]

	// Get rider location from Redis
	riderLoc, err := u.dispatcher.GetRiderLocation(ctx, t.TenantID, assignment.FleetMemberID)
	if err != nil {
		return // Rider has no location, skip
	}

	// Get dropoff location
	dropLat, dropLng, ok := extractDropoffLocation(t)
	if !ok {
		return // No dropoff location, skip
	}

	// Calculate route via routing service
	route, err := u.routingSvc.Route(ctx,
		routing.LatLng{Lat: riderLoc.Latitude, Lng: riderLoc.Longitude},
		routing.LatLng{Lat: dropLat, Lng: dropLng},
	)
	if err != nil {
		u.log.Debug("eta: routing failed",
			zap.String("task_id", t.ID.String()),
			zap.Error(err),
		)
		return
	}

	etaMinutes := route.DurationSeconds / 60
	distanceKm := route.DistanceMeters / 1000

	// Publish ETA update event
	if u.publisher != nil {
		if pubErr := u.publisher.PublishTaskETAUpdated(ctx, t.TenantID, events.TaskETAEventData{
			TaskID:            t.ID.String(),
			TrackingCode:      t.TrackingCode,
			ExternalReference: t.ExternalReference,
			ETAMinutes:        etaMinutes,
			DistanceKm:        distanceKm,
			RiderLat:          riderLoc.Latitude,
			RiderLng:          riderLoc.Longitude,
		}); pubErr != nil {
			u.log.Warn("eta: failed to publish event",
				zap.String("task_id", t.ID.String()),
				zap.Error(pubErr),
			)
		}
	}

	u.log.Debug("eta updated",
		zap.String("task_id", t.ID.String()),
		zap.Float64("eta_minutes", etaMinutes),
		zap.Float64("distance_km", distanceKm),
	)
}
