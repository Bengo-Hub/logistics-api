package dispatch

import (
	"context"
	"math"
	sharedcache "github.com/Bengo-Hub/cache"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/ent/task"
	"github.com/bengobox/logistics-service/internal/ent/taskassignment"
	"github.com/bengobox/logistics-service/internal/modules/routing"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
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
			q.Where(taskassignment.StatusIn("assigned", "accepted"))
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
	// Runs on every replica's ticker; only the first replica in each period does the work.
	if !sharedcache.ClaimPeriod(ctx, "logistics:eta-updater", u.interval) {
		return
	}
	// Every moving leg. This used to look only at the legacy accepted/en_route statuses, so
	// riders on the per-leg flow never got a periodic ETA. The assignment eager load also had
	// Limit(1), which limits the whole eager query, so only one task in the batch got its rider.
	activeTasks, err := u.entClient.Task.Query().
		Where(task.StatusIn("accepted", "en_route", "en_route_pickup", "arrived_pickup", "picked_up", "en_route_dropoff")).
		WithSteps().
		WithAssignments(func(q *ent.TaskAssignmentQuery) {
			q.Where(taskassignment.StatusIn("assigned", "accepted"))
		}).
		Order(ent.Asc(task.FieldUpdatedAt)).
		Limit(200).
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

	// Before pickup the rider is heading to the outlet; after it, to the customer.
	var dropLat, dropLng float64
	var ok bool
	if tasks.IsPrePickup(t.Status) {
		dropLat, dropLng, ok = extractPickupLocation(t)
	} else {
		dropLat, dropLng, ok = extractDropoffLocation(t)
	}
	if !ok {
		return
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

	// Keep the latest ETA for the tracking endpoint, and publish only when it moved by a
	// minute or more: one outbox event per task every 30 seconds was pure churn.
	prev, hadPrev := u.dispatcher.LastETA(ctx, t.ID)
	u.dispatcher.SaveETA(ctx, t.ID, ETA{Minutes: etaMinutes, DistanceKm: distanceKm, At: time.Now().UTC()})
	if hadPrev && math.Abs(prev.Minutes-etaMinutes) < 1 {
		return
	}
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
