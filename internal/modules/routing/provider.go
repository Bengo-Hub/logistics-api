package routing

import (
	"context"
	"fmt"
)

// LatLng represents a geographic coordinate.
type LatLng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// Route represents a calculated route between two or more points.
type Route struct {
	// GeoJSON LineString coordinates [lng, lat]
	Coordinates [][2]float64 `json:"coordinates"`
	// Total distance in meters
	DistanceMeters float64 `json:"distance_meters"`
	// Total duration in seconds
	DurationSeconds float64 `json:"duration_seconds"`
	// Encoded polyline (if available)
	Polyline string `json:"polyline,omitempty"`
}

// MatrixEntry represents a single distance/duration between two points.
type MatrixEntry struct {
	DistanceMeters  float64 `json:"distance_meters"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// Isochrone represents a reachability polygon.
type Isochrone struct {
	// GeoJSON Polygon coordinates
	Coordinates [][][2]float64 `json:"coordinates"`
	// Contour time in minutes
	TimeMinutes float64 `json:"time_minutes"`
}

// Provider defines the interface for routing engines (Valhalla, OSRM, Google Maps).
type Provider interface {
	// Name returns the provider identifier (e.g., "valhalla", "osrm").
	Name() string

	// Route calculates a route between two points.
	Route(ctx context.Context, origin, destination LatLng) (*Route, error)

	// ETA returns estimated travel time in seconds between two points.
	ETA(ctx context.Context, origin, destination LatLng) (float64, error)

	// Distance returns estimated distance in meters between two points.
	Distance(ctx context.Context, origin, destination LatLng) (float64, error)

	// Matrix calculates a distance/duration matrix for multiple origins and destinations.
	Matrix(ctx context.Context, origins, destinations []LatLng) ([][]MatrixEntry, error)

	// Isochrone calculates a reachability polygon from a point within a given time in minutes.
	Isochrone(ctx context.Context, origin LatLng, timeMinutes float64) (*Isochrone, error)

	// Health checks if the routing engine is reachable.
	Health(ctx context.Context) error
}

// ErrProviderUnavailable is returned when a routing provider is unreachable.
var ErrProviderUnavailable = fmt.Errorf("routing provider unavailable")
