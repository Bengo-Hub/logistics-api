package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Service orchestrates routing requests with caching and fallback.
type Service struct {
	primary  Provider
	fallback Provider
	redis    *redis.Client
	cacheTTL time.Duration
	log      *zap.Logger
}

// NewService creates a new routing service with primary and optional fallback provider.
func NewService(primary Provider, fallback Provider, rdb *redis.Client, cacheTTL time.Duration, log *zap.Logger) *Service {
	return &Service{
		primary:  primary,
		fallback: fallback,
		redis:    rdb,
		cacheTTL: cacheTTL,
		log:      log.Named("routing.service"),
	}
}

// Route calculates a route, using cache and fallback.
func (s *Service) Route(ctx context.Context, origin, destination LatLng) (*Route, error) {
	// Check cache
	cacheKey := fmt.Sprintf("route:%f,%f:%f,%f", origin.Lat, origin.Lng, destination.Lat, destination.Lng)
	if cached, err := s.getFromCache(ctx, cacheKey); err == nil {
		var route Route
		if json.Unmarshal(cached, &route) == nil {
			return &route, nil
		}
	}

	// Try primary
	route, err := s.primary.Route(ctx, origin, destination)
	if err != nil {
		s.log.Warn("primary routing failed, trying fallback",
			zap.String("provider", s.primary.Name()),
			zap.Error(err),
		)
		if s.fallback != nil {
			route, err = s.fallback.Route(ctx, origin, destination)
			if err != nil {
				return nil, fmt.Errorf("routing: all providers failed: %w", err)
			}
		} else {
			return nil, fmt.Errorf("routing: %s failed: %w", s.primary.Name(), err)
		}
	}

	// Cache result
	s.setCache(ctx, cacheKey, route)
	return route, nil
}

// ETA returns estimated travel time in seconds.
func (s *Service) ETA(ctx context.Context, origin, destination LatLng) (float64, error) {
	route, err := s.Route(ctx, origin, destination)
	if err != nil {
		return 0, err
	}
	return route.DurationSeconds, nil
}

// ETAMinutes returns ETA in minutes (rounded).
func (s *Service) ETAMinutes(ctx context.Context, origin, destination LatLng) (float64, error) {
	secs, err := s.ETA(ctx, origin, destination)
	if err != nil {
		return 0, err
	}
	return secs / 60, nil
}

// Distance returns distance in meters.
func (s *Service) Distance(ctx context.Context, origin, destination LatLng) (float64, error) {
	route, err := s.Route(ctx, origin, destination)
	if err != nil {
		return 0, err
	}
	return route.DistanceMeters, nil
}

// DistanceKm returns distance in kilometers.
func (s *Service) DistanceKm(ctx context.Context, origin, destination LatLng) (float64, error) {
	m, err := s.Distance(ctx, origin, destination)
	if err != nil {
		return 0, err
	}
	return m / 1000, nil
}

// Matrix calculates a distance/duration matrix.
func (s *Service) Matrix(ctx context.Context, origins, destinations []LatLng) ([][]MatrixEntry, error) {
	matrix, err := s.primary.Matrix(ctx, origins, destinations)
	if err != nil && s.fallback != nil {
		s.log.Warn("primary matrix failed, trying fallback", zap.Error(err))
		return s.fallback.Matrix(ctx, origins, destinations)
	}
	return matrix, err
}

// Isochrone calculates a reachability polygon.
func (s *Service) Isochrone(ctx context.Context, origin LatLng, timeMinutes float64) (*Isochrone, error) {
	iso, err := s.primary.Isochrone(ctx, origin, timeMinutes)
	if err != nil && s.fallback != nil {
		s.log.Warn("primary isochrone failed, trying fallback", zap.Error(err))
		return s.fallback.Isochrone(ctx, origin, timeMinutes)
	}
	return iso, err
}

// Health checks all providers.
func (s *Service) Health(ctx context.Context) map[string]error {
	result := map[string]error{
		s.primary.Name(): s.primary.Health(ctx),
	}
	if s.fallback != nil {
		result[s.fallback.Name()] = s.fallback.Health(ctx)
	}
	return result
}

func (s *Service) getFromCache(ctx context.Context, key string) ([]byte, error) {
	if s.redis == nil {
		return nil, fmt.Errorf("no cache")
	}
	return s.redis.Get(ctx, key).Bytes()
}

func (s *Service) setCache(ctx context.Context, key string, value any) {
	if s.redis == nil {
		return
	}
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	if err := s.redis.Set(ctx, key, data, s.cacheTTL).Err(); err != nil {
		s.log.Debug("failed to cache route", zap.Error(err))
	}
}
