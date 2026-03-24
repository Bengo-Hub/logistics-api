package dispatch

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/ent"
	"github.com/bengobox/logistics-service/internal/modules/fleet"
	"github.com/bengobox/logistics-service/internal/modules/tasks"
)

const (
	// riderLocationKey is the Redis GEO key for rider locations per tenant.
	riderLocationKey = "logistics:riders:geo:%s"
	// maxDispatchRadiusKm is the maximum distance (km) to consider a rider.
	maxDispatchRadiusKm = 15.0
)

// AutoDispatcher finds and assigns the nearest available rider for a task.
type AutoDispatcher struct {
	log      *zap.Logger
	fleetSvc *fleet.Service
	taskSvc  *tasks.Service
	redis    *redis.Client
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

// RiderLocation represents a rider's current GPS position stored in Redis.
type RiderLocation struct {
	MemberID  uuid.UUID
	Latitude  float64
	Longitude float64
}

// UpdateRiderLocation stores a rider's current location in Redis GEO.
func (d *AutoDispatcher) UpdateRiderLocation(ctx context.Context, tenantID, memberID uuid.UUID, lat, lng float64) error {
	key := fmt.Sprintf(riderLocationKey, tenantID.String())
	return d.redis.GeoAdd(ctx, key, &redis.GeoLocation{
		Name:      memberID.String(),
		Longitude: lng,
		Latitude:  lat,
	}).Err()
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
		return nil // Can't dispatch without a pickup location
	}

	// 3. Get all active fleet members for this tenant
	members, err := d.fleetSvc.ListMembers(ctx, tenantID, "active")
	if err != nil {
		return fmt.Errorf("dispatch: list members: %w", err)
	}

	if len(members) == 0 {
		d.log.Warn("no active riders for auto-dispatch",
			zap.String("task_id", taskID.String()),
		)
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

	if len(candidates) == 0 {
		d.log.Warn("no riders with location available for auto-dispatch",
			zap.String("task_id", taskID.String()),
		)
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
