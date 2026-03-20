package routing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ValhallaProvider implements Provider using the Valhalla routing engine.
type ValhallaProvider struct {
	baseURL string
	client  *http.Client
}

// NewValhallaProvider creates a new Valhalla routing provider.
func NewValhallaProvider(baseURL string, timeout time.Duration) *ValhallaProvider {
	return &ValhallaProvider{
		baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

func (v *ValhallaProvider) Name() string { return "valhalla" }

func (v *ValhallaProvider) Route(ctx context.Context, origin, destination LatLng) (*Route, error) {
	reqBody := fmt.Sprintf(`{
		"locations": [
			{"lat": %f, "lon": %f},
			{"lat": %f, "lon": %f}
		],
		"costing": "auto",
		"directions_options": {"units": "kilometers"},
		"shape_match": "map_snap"
	}`, origin.Lat, origin.Lng, destination.Lat, destination.Lng)

	resp, err := v.doRequest(ctx, "/route", reqBody)
	if err != nil {
		return nil, err
	}

	// Parse Valhalla route response
	trip, ok := resp["trip"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("valhalla: invalid trip response")
	}

	summary, _ := trip["summary"].(map[string]any)
	distanceKm, _ := summary["length"].(float64)
	durationSec, _ := summary["time"].(float64)

	// Extract shape (encoded polyline) and decode to coordinates
	legs, _ := trip["legs"].([]any)
	var coords [][2]float64
	for _, leg := range legs {
		legMap, _ := leg.(map[string]any)
		shape, _ := legMap["shape"].(string)
		if shape != "" {
			coords = append(coords, decodePolyline(shape)...)
		}
	}

	return &Route{
		Coordinates:     coords,
		DistanceMeters:  distanceKm * 1000,
		DurationSeconds: durationSec,
	}, nil
}

func (v *ValhallaProvider) ETA(ctx context.Context, origin, destination LatLng) (float64, error) {
	route, err := v.Route(ctx, origin, destination)
	if err != nil {
		return 0, err
	}
	return route.DurationSeconds, nil
}

func (v *ValhallaProvider) Distance(ctx context.Context, origin, destination LatLng) (float64, error) {
	route, err := v.Route(ctx, origin, destination)
	if err != nil {
		return 0, err
	}
	return route.DistanceMeters, nil
}

func (v *ValhallaProvider) Matrix(ctx context.Context, origins, destinations []LatLng) ([][]MatrixEntry, error) {
	sources := make([]map[string]float64, len(origins))
	for i, o := range origins {
		sources[i] = map[string]float64{"lat": o.Lat, "lon": o.Lng}
	}
	targets := make([]map[string]float64, len(destinations))
	for i, d := range destinations {
		targets[i] = map[string]float64{"lat": d.Lat, "lon": d.Lng}
	}

	payload := map[string]any{
		"sources": sources,
		"targets": targets,
		"costing": "auto",
	}
	body, _ := json.Marshal(payload)

	resp, err := v.doRequest(ctx, "/sources_to_targets", string(body))
	if err != nil {
		return nil, err
	}

	rawMatrix, ok := resp["sources_to_targets"].([]any)
	if !ok {
		return nil, fmt.Errorf("valhalla: invalid matrix response")
	}

	matrix := make([][]MatrixEntry, len(rawMatrix))
	for i, row := range rawMatrix {
		rowArr, _ := row.([]any)
		matrix[i] = make([]MatrixEntry, len(rowArr))
		for j, cell := range rowArr {
			cellMap, _ := cell.(map[string]any)
			dist, _ := cellMap["distance"].(float64)
			dur, _ := cellMap["time"].(float64)
			matrix[i][j] = MatrixEntry{
				DistanceMeters:  dist * 1000, // Valhalla returns km
				DurationSeconds: dur,
			}
		}
	}

	return matrix, nil
}

func (v *ValhallaProvider) Isochrone(ctx context.Context, origin LatLng, timeMinutes float64) (*Isochrone, error) {
	reqBody := fmt.Sprintf(`{
		"locations": [{"lat": %f, "lon": %f}],
		"costing": "auto",
		"contours": [{"time": %f}],
		"polygons": true
	}`, origin.Lat, origin.Lng, timeMinutes)

	resp, err := v.doRequest(ctx, "/isochrone", reqBody)
	if err != nil {
		return nil, err
	}

	// Parse GeoJSON response
	features, _ := resp["features"].([]any)
	if len(features) == 0 {
		return nil, fmt.Errorf("valhalla: no isochrone features returned")
	}

	feature, _ := features[0].(map[string]any)
	geometry, _ := feature["geometry"].(map[string]any)
	rawCoords, _ := geometry["coordinates"].([]any)

	var coords [][][2]float64
	for _, ring := range rawCoords {
		ringArr, _ := ring.([]any)
		var ringCoords [][2]float64
		for _, pt := range ringArr {
			ptArr, _ := pt.([]any)
			if len(ptArr) >= 2 {
				lng, _ := ptArr[0].(float64)
				lat, _ := ptArr[1].(float64)
				ringCoords = append(ringCoords, [2]float64{lng, lat})
			}
		}
		coords = append(coords, ringCoords)
	}

	return &Isochrone{
		Coordinates: coords,
		TimeMinutes: timeMinutes,
	}, nil
}

func (v *ValhallaProvider) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.baseURL+"/status", nil)
	if err != nil {
		return ErrProviderUnavailable
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return ErrProviderUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrProviderUnavailable
	}
	return nil
}

func (v *ValhallaProvider) doRequest(ctx context.Context, path, body string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.baseURL+path, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("valhalla: failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProviderUnavailable, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("valhalla: failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("valhalla: HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("valhalla: failed to decode response: %w", err)
	}

	return result, nil
}

// decodePolyline decodes a Valhalla encoded polyline (6 decimal precision).
func decodePolyline(encoded string) [][2]float64 {
	var coords [][2]float64
	var lat, lng int
	idx := 0

	for idx < len(encoded) {
		var dlat, dlng int

		shift := 0
		result := 0
		for {
			b := int(encoded[idx]) - 63
			idx++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		if result&1 != 0 {
			dlat = ^(result >> 1)
		} else {
			dlat = result >> 1
		}
		lat += dlat

		shift = 0
		result = 0
		for {
			b := int(encoded[idx]) - 63
			idx++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		if result&1 != 0 {
			dlng = ^(result >> 1)
		} else {
			dlng = result >> 1
		}
		lng += dlng

		// Valhalla uses 6 decimal precision
		coords = append(coords, [2]float64{float64(lng) / 1e6, float64(lat) / 1e6})
	}

	return coords
}
