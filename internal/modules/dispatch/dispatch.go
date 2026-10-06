package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	"math"
	"sort"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/modules/fleet"
	notifmod "github.com/bengobox/logistics-service/internal/modules/notifications"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

const (
	// riderLocationKey is the Redis GEO key for rider locations per tenant.
	riderLocationKey = "logistics:riders:geo:%s"
	// maxDispatchRadiusKm is the maximum distance (km) to consider a rider.
	maxDispatchRadiusKm = 15.0
	// riderSeenKey is a sorted set of rider ids scored by the unix time of their last fix.
	riderSeenKey = "logistics:riders:seen:%s"
	// taskETAKey holds the latest ETA computed for a task.
	taskETAKey = "logistics:eta:%s"
	// maxFixAge is how old a rider's last position may be for auto-dispatch to use it.
	maxFixAge = 10 * time.Minute
)

// AutoDispatcher finds and assigns the nearest available rider for a task.
type AutoDispatcher struct {
	log      *zap.Logger
	fleetSvc *fleet.Service
	taskSvc  *tasks.Service
	redis    *redis.Client
	notifSvc *notifmod.Service
}

// NewAutoDispatcher creates a new auto-dispatcher.
func NewAutoDispatcher(log *zap.Logger, fleetSvc *fleet.Service, taskSvc *tasks.Service, rdb *redis.Client) *AutoDispatcher {
	return &AutoDispatcher{
		log:      log.Named("dispatch.auto"),
		fleetSvc: fleetSvc,
		taskSvc:  taskSvc,
		redis:    rdb,
	}
}

// SetNotifications wires the dispatcher-alert service so a task that couldn't be
// auto-dispatched raises a notification instead of only a log line — previously a
// dispatcher had no way to know a task needed manual assignment short of noticing it
// still pending on the Tasks page.
func (d *AutoDispatcher) SetNotifications(svc *notifmod.Service) { d.notifSvc = svc }

// notifyDispatchFailed records why auto-dispatch couldn't assign taskID, deduplicated so a
// task that keeps failing to auto-dispatch (e.g. retried by a scheduler) doesn't spam one
// notification per attempt.
func (d *AutoDispatcher) notifyDispatchFailed(ctx context.Context, tenantID, taskID uuid.UUID, reason string) {
	if d.notifSvc == nil {
		return
	}
	if _, err := d.notifSvc.CreateDeduped(ctx, notifmod.CreateRequest{
		TenantID:         tenantID,
		NotificationType: "dispatch_failed",
		Title:            "Task needs manual assignment",
		Body:             reason,
		Payload: map[string]any{
			"task_id": taskID.String(),
			"reason":  reason,
		},
		RelatedTaskID: &taskID,
	}); err != nil {
		d.log.Error("failed to create dispatch-failed notification",
			zap.String("task_id", taskID.String()), zap.Error(err))
	}
}

// RiderLocation represents a rider's current GPS position stored in Redis.
type RiderLocation struct {
	MemberID  uuid.UUID
	Latitude  float64
	Longitude float64
	// SeenAt is when the rider last reported a position (zero when unknown).
	SeenAt time.Time
}

// UpdateRiderLocation stores a rider's current location in Redis GEO and when it was reported.
// GEO members never expire, so without the last-seen time a rider who switched the app off
// yesterday still looked like the nearest rider and kept getting auto-assigned.
func (d *AutoDispatcher) UpdateRiderLocation(ctx context.Context, tenantID, memberID uuid.UUID, lat, lng float64) error {
	key := fmt.Sprintf(riderLocationKey, tenantID.String())
	pipe := d.redis.TxPipeline()
	pipe.GeoAdd(ctx, key, &redis.GeoLocation{
		Name:      memberID.String(),
		Longitude: lng,
		Latitude:  lat,
	})
	pipe.ZAdd(ctx, fmt.Sprintf(riderSeenKey, tenantID.String()), redis.Z{
		Score: float64(time.Now().Unix()), Member: memberID.String(),
	})
	_, err := pipe.Exec(ctx)
	return err
}

// riderSeen returns when each of the given riders last reported a position.
func (d *AutoDispatcher) riderSeen(ctx context.Context, tenantID uuid.UUID, ids []string) map[string]time.Time {
	out := map[string]time.Time{}
	if len(ids) == 0 {
		return out
	}
	scores, err := d.redis.ZMScore(ctx, fmt.Sprintf(riderSeenKey, tenantID.String()), ids...).Result()
	if err != nil {
		return out
	}
	for i, s := range scores {
		if s > 0 {
			out[ids[i]] = time.Unix(int64(s), 0).UTC()
		}
	}
	return out
}

// freshCandidates keeps riders who reported a position within maxFix of now and who are not
// excluded (they declined this job). Riders with no last-seen time are dropped too.
func freshCandidates(cands []riderCandidate, seen map[string]time.Time, exclude map[uuid.UUID]bool, now time.Time, maxFix time.Duration) []riderCandidate {
	out := make([]riderCandidate, 0, len(cands))
	for _, c := range cands {
		if exclude[c.MemberID] {
			continue
		}
		at, ok := seen[c.MemberID.String()]
		if !ok || now.Sub(at) > maxFix {
			continue
		}
		out = append(out, c)
	}
	return out
}

// ETA is the last computed arrival estimate for a task.
type ETA struct {
	Minutes    float64   `json:"minutes"`
	DistanceKm float64   `json:"distance_km"`
	At         time.Time `json:"at"`
}

// SaveETA keeps the latest ETA for a task for the tracking endpoint (10 minute expiry, so a
// stale estimate disappears once updates stop).
func (d *AutoDispatcher) SaveETA(ctx context.Context, taskID uuid.UUID, eta ETA) {
	if d.redis == nil {
		return
	}
	b, _ := json.Marshal(eta)
	_ = d.redis.Set(ctx, fmt.Sprintf(taskETAKey, taskID.String()), b, 10*time.Minute).Err()
}

// LastETA returns the latest saved ETA for a task.
func (d *AutoDispatcher) LastETA(ctx context.Context, taskID uuid.UUID) (ETA, bool) {
	var eta ETA
	if d.redis == nil {
		return eta, false
	}
	b, err := d.redis.Get(ctx, fmt.Sprintf(taskETAKey, taskID.String())).Bytes()
	if err != nil || json.Unmarshal(b, &eta) != nil {
		return eta, false
	}
	return eta, true
}

// GetRiderLocation retrieves a rider's last known location from Redis.
func (d *AutoDispatcher) GetRiderLocation(ctx context.Context, tenantID, memberID uuid.UUID) (*RiderLocation, error) {
	key := fmt.Sprintf(riderLocationKey, tenantID.String())
	positions, err := d.redis.GeoPos(ctx, key, memberID.String()).Result()
	if err != nil {
		return nil, fmt.Errorf("dispatch: get rider location: %w", err)
	}
	if len(positions) == 0 || positions[0] == nil {
		return nil, fmt.Errorf("dispatch: rider location not found")
	}
	return &RiderLocation{
		MemberID:  memberID,
		Latitude:  positions[0].Latitude,
		Longitude: positions[0].Longitude,
		SeenAt:    d.riderSeen(ctx, tenantID, []string{memberID.String()})[memberID.String()],
	}, nil
}

// DispatchTask finds the nearest available rider and assigns them to the task.
// It returns nil if no riders are available (task stays in pending for manual assignment).
func (d *AutoDispatcher) DispatchTask(ctx context.Context, tenantID, taskID uuid.UUID) error {
	// 1. Get task details with steps
	t, err := d.taskSvc.GetTask(ctx, tenantID, taskID)
	if err != nil {
		return fmt.Errorf("dispatch: get task: %w", err)
	}

	// Only dispatch pending tasks
	if t.Status != "pending" {
		d.log.Debug("skipping dispatch, task not pending",
			zap.String("task_id", taskID.String()),
			zap.String("status", t.Status),
		)
		return nil
	}

	// 2. Extract pickup location from task steps or metadata
	pickupLat, pickupLng, ok := extractPickupLocation(t)
	if !ok {
		d.log.Warn("no pickup location for auto-dispatch",
			zap.String("task_id", taskID.String()),
		)
		d.notifyDispatchFailed(ctx, tenantID, taskID, "No pickup location on the task — auto-dispatch can't route it.")
		return nil // Can't dispatch without a pickup location
	}

	// 3. Get all active fleet members for this tenant
	members, _, err := d.fleetSvc.ListMembers(ctx, tenantID, "active", "", 1000, 0)
	if err != nil {
		return fmt.Errorf("dispatch: list members: %w", err)
	}

	if len(members) == 0 {
		d.log.Warn("no active riders for auto-dispatch",
			zap.String("task_id", taskID.String()),
		)
		d.notifyDispatchFailed(ctx, tenantID, taskID, "No active riders in the fleet to assign.")
		return nil
	}

	// 4. Find nearest riders using Redis GEO
	candidates, err := d.findNearestRiders(ctx, tenantID, members, pickupLat, pickupLng)
	if err != nil {
		d.log.Warn("could not find nearest riders, falling back to haversine",
			zap.Error(err),
		)
		candidates = d.fallbackHaversine(ctx, tenantID, members, pickupLat, pickupLng)
	}

	// Only riders with a recent fix, and never a rider who already declined this job.
	ids := make([]string, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.MemberID.String())
	}
	candidates = freshCandidates(candidates, d.riderSeen(ctx, tenantID, ids), d.taskSvc.DeclinedMembers(ctx, taskID), time.Now(), maxFixAge)

	if len(candidates) == 0 {
		d.log.Warn("no riders with location available for auto-dispatch",
			zap.String("task_id", taskID.String()),
		)
		d.notifyDispatchFailed(ctx, tenantID, taskID, "No available rider has reported a live GPS location in the last 10 minutes.")
		return nil
	}

	// 5. Sort by distance (nearest first)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].DistanceKm < candidates[j].DistanceKm
	})

	// 6. Filter by max radius
	var eligible []riderCandidate
	for _, c := range candidates {
		if c.DistanceKm <= maxDispatchRadiusKm {
			eligible = append(eligible, c)
		}
	}

	if len(eligible) == 0 {
		d.log.Warn("no riders within dispatch radius",
			zap.String("task_id", taskID.String()),
			zap.Float64("radius_km", maxDispatchRadiusKm),
		)
		d.notifyDispatchFailed(ctx, tenantID, taskID,
			fmt.Sprintf("No riders within %.0f km of the pickup location.", maxDispatchRadiusKm))
		return nil
	}

	// 7. Assign to nearest rider
	nearest := eligible[0]
	_, err = d.taskSvc.AssignTask(ctx, tenantID, taskID, tasks.AssignTaskRequest{
		FleetMemberID: nearest.MemberID,
	})
	if err != nil {
		return fmt.Errorf("dispatch: assign failed: %w", err)
	}

	d.log.Info("auto-dispatched task",
		zap.String("task_id", taskID.String()),
		zap.String("rider_id", nearest.MemberID.String()),
		zap.Float64("distance_km", nearest.DistanceKm),
	)

	return nil
}

type riderCandidate struct {
	MemberID   uuid.UUID
	DistanceKm float64
}

// findNearestRiders uses Redis GEOSEARCH to find riders near the pickup location.
func (d *AutoDispatcher) findNearestRiders(ctx context.Context, tenantID uuid.UUID, members []*ent.FleetMember, lat, lng float64) ([]riderCandidate, error) {
	key := fmt.Sprintf(riderLocationKey, tenantID.String())

	results, err := d.redis.GeoSearch(ctx, key, &redis.GeoSearchQuery{
		Longitude:  lng,
		Latitude:   lat,
		Radius:     maxDispatchRadiusKm,
		RadiusUnit: "km",
		Sort:       "ASC",
		Count:      20,
	}).Result()
	if err != nil {
		return nil, fmt.Errorf("geo search: %w", err)
	}

	// Build set of active member IDs for filtering
	activeMembers := make(map[string]bool, len(members))
	for _, m := range members {
		activeMembers[m.ID.String()] = true
	}

	// Get distances using GeoSearchLocation
	locations, err := d.redis.GeoSearchLocation(ctx, key, &redis.GeoSearchLocationQuery{
		GeoSearchQuery: redis.GeoSearchQuery{
			Longitude:  lng,
			Latitude:   lat,
			Radius:     maxDispatchRadiusKm,
			RadiusUnit: "km",
			Sort:       "ASC",
			Count:      20,
		},
		WithDist: true,
	}).Result()
	if err != nil {
		// Fall back to results without distance
		var candidates []riderCandidate
		for _, name := range results {
			if !activeMembers[name] {
				continue
			}
			memberID, parseErr := uuid.Parse(name)
			if parseErr != nil {
				continue
			}
			candidates = append(candidates, riderCandidate{MemberID: memberID})
		}
		return candidates, nil
	}

	var candidates []riderCandidate
	for _, loc := range locations {
		if !activeMembers[loc.Name] {
			continue
		}
		memberID, parseErr := uuid.Parse(loc.Name)
		if parseErr != nil {
			continue
		}
		candidates = append(candidates, riderCandidate{
			MemberID:   memberID,
			DistanceKm: loc.Dist,
		})
	}

	return candidates, nil
}

// fallbackHaversine computes distances using the haversine formula when Redis GEO is unavailable.
func (d *AutoDispatcher) fallbackHaversine(ctx context.Context, tenantID uuid.UUID, members []*ent.FleetMember, lat, lng float64) []riderCandidate {
	key := fmt.Sprintf(riderLocationKey, tenantID.String())
	var candidates []riderCandidate

	for _, m := range members {
		positions, err := d.redis.GeoPos(ctx, key, m.ID.String()).Result()
		if err != nil || len(positions) == 0 || positions[0] == nil {
			continue
		}
		dist := haversineKm(lat, lng, positions[0].Latitude, positions[0].Longitude)
		candidates = append(candidates, riderCandidate{
			MemberID:   m.ID,
			DistanceKm: dist,
		})
	}

	return candidates
}

// extractPickupLocation extracts lat/lng from the task's pickup step address_json.
// The address_json is expected to have "latitude"/"longitude" or "lat"/"lng" keys.
func extractPickupLocation(t *ent.Task) (lat, lng float64, ok bool) {
	if t.Edges.Steps == nil {
		// Try task metadata as fallback
		return extractCoordsFromMap(t.Metadata)
	}

	// Find pickup step (lowest sequence or step_type == "pickup")
	for _, step := range t.Edges.Steps {
		if step.StepType == "pickup" && step.AddressJSON != nil {
			lat, lng, ok = extractCoordsFromMap(step.AddressJSON)
			if ok {
				return lat, lng, true
			}
		}
	}

	// Fallback: task-level metadata
	return extractCoordsFromMap(t.Metadata)
}

// extractDropoffLocation extracts lat/lng from the task's dropoff step address_json.
func extractDropoffLocation(t *ent.Task) (lat, lng float64, ok bool) {
	if t.Edges.Steps == nil {
		return 0, 0, false
	}

	for _, step := range t.Edges.Steps {
		if step.StepType == "dropoff" && step.AddressJSON != nil {
			lat, lng, ok = extractCoordsFromMap(step.AddressJSON)
			if ok {
				return lat, lng, true
			}
		}
	}

	return 0, 0, false
}

// extractCoordsFromMap extracts latitude/longitude from a map with various key names.
func extractCoordsFromMap(m map[string]any) (lat, lng float64, ok bool) {
	if m == nil {
		return 0, 0, false
	}

	lat, latOk := toFloat64(m["latitude"])
	if !latOk {
		lat, latOk = toFloat64(m["lat"])
	}
	if !latOk {
		lat, latOk = toFloat64(m["pickup_lat"])
	}

	lng, lngOk := toFloat64(m["longitude"])
	if !lngOk {
		lng, lngOk = toFloat64(m["lng"])
	}
	if !lngOk {
		lng, lngOk = toFloat64(m["pickup_lng"])
	}

	return lat, lng, latOk && lngOk
}

func toFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	default:
		return 0, false
	}
}

// haversineKm calculates the great-circle distance between two points in kilometers.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	const earthRadiusKm = 6371.0

	dLat := degToRad(lat2 - lat1)
	dLng := degToRad(lng2 - lng1)

	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(degToRad(lat1))*math.Cos(degToRad(lat2))*
			math.Sin(dLng/2)*math.Sin(dLng/2)

	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadiusKm * c
}

func degToRad(deg float64) float64 {
	return deg * math.Pi / 180
}
