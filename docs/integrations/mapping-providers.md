# Mapping & Routing Providers – Integration Notes

This guide describes how the Logistics Service integrates with mapping, routing, and road-condition providers to deliver accurate geocoding, routing, ETAs, and incident-aware alternative route suggestions.

## Architecture

The routing stack uses a **free-first** approach: self-hosted open-source engines as primary, with paid providers available as fallbacks.

```
Frontend (MapLibre GL JS via @bengo-hub/maps)
      |
      | vector tiles
      |
TileServer-GL (tiles.codevertexitsolutions.com)
      |
      | OpenStreetMap Kenya mbtiles
      |
logistics-api (Go)
      |
      | route / ETA / matrix / isochrone
      |
Valhalla routing engine (ClusterIP: valhalla:8002)
      |
      | OpenStreetMap Kenya PBF (Geofabrik, weekly refresh)
```

## Providers

### Valhalla (Primary - FREE)
- **Status**: Deployed in-cluster (`valhalla.logistics.svc.cluster.local:8002`)
- **Data**: Kenya OSM extract from Geofabrik (~200-300 MB PBF)
- **Features**: routing, ETA, distance matrix, isochrones, map matching, traffic-aware patterns
- **Used by**: Mapbox internally, Uber-style routing systems
- **Config**: `ROUTING_PRIMARY_URL` env var
- **Refresh**: Weekly CronJob (Monday 2AM UTC) downloads latest PBF

### OSRM + OpenStreetMap (Fallback - FREE)
- Open-source routing engine backed by OSM data.
- Pros: extremely fast (~10ms queries), no per-request charges.
- Cons: less flexible than Valhalla, no isochrones/elevation.
- Deployment: planned as secondary fallback via `ROUTING_FALLBACK_URL`.

### Google Maps Platform (Paid Fallback)
- Services: Geocoding, Routes/Directions API, Distance Matrix, Traffic Incidents, Speed Limits.
- Authentication: API key stored as `provider_credentials`.
- Use case: highest accuracy when free providers insufficient.
- Rate limiting: enforce QPS caps; exponential backoff on HTTP 429/5xx.
- **Not required for MVP** - Valhalla covers all routing needs.

### TileServer-GL (Map Rendering - FREE)
- **Status**: Deployed in-cluster (`tileserver.logistics.svc.cluster.local:8080`)
- **Public URL**: `https://tiles.codevertexitsolutions.com`
- **Purpose**: Serve vector map tiles to MapLibre GL JS frontends
- **Data**: Kenya OpenMapTiles (mbtiles format)

## Provider Abstraction

The routing module (`internal/modules/routing/`) implements a `Provider` interface:

```go
type Provider interface {
    Name() string
    Route(ctx, origin, destination LatLng) (*Route, error)
    ETA(ctx, origin, destination LatLng) (float64, error)
    Distance(ctx, origin, destination LatLng) (float64, error)
    Matrix(ctx, origins, destinations []LatLng) ([][]MatrixEntry, error)
    Isochrone(ctx, origin LatLng, timeMinutes float64) (*Isochrone, error)
    Health(ctx) error
}
```

The `Service` layer adds Redis caching (TTL configurable via `ROUTING_CACHE_TTL`) and automatic fallback from primary to secondary provider.

## API Endpoints

All routing endpoints require authentication and are scoped to tenant:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/{tenant}/routing/route?from_lat=&from_lng=&to_lat=&to_lng=` | Calculate route with geometry |
| GET | `/{tenant}/routing/eta?from_lat=&from_lng=&to_lat=&to_lng=` | Get ETA in minutes + distance in km |
| POST | `/{tenant}/routing/matrix` | Distance/duration matrix (batch) |
| GET | `/{tenant}/routing/isochrone?lat=&lng=&time_minutes=` | Reachability polygon |
| GET | `/{tenant}/routing/health` | Provider health status |

## Public Tracking

A public endpoint (no auth required) allows tracking by waybill/tracking code:

| Method | Path | Description |
|--------|------|-------------|
| GET | `/api/v1/track/{trackingCode}` | Public delivery tracking by code |

Tracking codes are auto-generated on task creation in format `CV-YYYYMMDD-XXXXXX`.

## Configuration

Environment variables:

```bash
ROUTING_PRIMARY_URL=http://valhalla.logistics.svc.cluster.local:8002  # Valhalla
ROUTING_FALLBACK_URL=                                                  # Optional OSRM/Google
ROUTING_REQUEST_TIMEOUT=10s
ROUTING_CACHE_TTL=5m
```

Per-tenant provider settings via `/v1/{tenant}/integrations/providers` (future):

```json
{
  "code": "valhalla",
  "enabled": true,
  "settings": {
    "baseUrl": "http://valhalla:8002",
    "profile": "auto"
  }
}
```

## Rate Limiting

All routing and tracking endpoints are rate-limited per tenant based on their subscription plan. Limits are read from JWT `subscription_limits` claims and enforced via Redis sliding window counters.

| Feature Key | Starter | Growth | Professional |
|-------------|---------|--------|--------------|
| `routing_requests_per_day` | 100 | 1,000 | 10,000 |
| `live_tracking_requests_per_day` | 500 | 5,000 | Unlimited |
| `live_tracking_duration_minutes` | 30 | 120 | Unlimited |
| `map_loads_per_day` | 200 | 2,000 | Unlimited |

When a limit is exceeded, the API returns `429 Too Many Requests` with:
- `X-RateLimit-Limit` / `X-RateLimit-Remaining` / `X-RateLimit-Feature` headers
- JSON body with `upgrade_url` for tenant admins

## Error Handling
- 4xx: validation errors → return `400` with safe messages.
- 5xx: transient provider outage → retry with backoff; fail over to fallback provider.
- Telemetry: export provider latency, success rate, and error rate to Prometheus.

## Security & Compliance
- All requests to external providers originate from server-side; never expose raw keys to clients.
- Tile server serves public tiles with CORS enabled and cache headers (24h browser, 7d CDN).
- Public tracking endpoint is rate-limited per tenant subscription plan.
