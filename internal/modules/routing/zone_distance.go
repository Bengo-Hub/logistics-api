package routing

import (
	"context"

	"github.com/bengobox/logistics-service/internal/modules/zones/geo"
)

// ZoneDistance adapts the routing service to the zones package so delivery quotes use
// cached road distances without the zones package depending on routing internals.
type ZoneDistance struct{ Svc *Service }

// RoadKm returns the road distance in kilometres between two points.
func (d ZoneDistance) RoadKm(ctx context.Context, from, to geo.Point) (float64, error) {
	return d.Svc.DistanceKm(ctx, LatLng{Lat: from.Lat, Lng: from.Lng}, LatLng{Lat: to.Lat, Lng: to.Lng})
}
