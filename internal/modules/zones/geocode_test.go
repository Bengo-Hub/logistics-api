package zones

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/bengobox/logistics-service/internal/modules/zones/geo"
)

type fakeAreas struct {
	cov  Coverage
	near *ZoneRef
	km   float64
}

func (f fakeAreas) Coverage(context.Context, uuid.UUID, uuid.UUID) (Coverage, error) { return f.cov, nil }
func (f fakeAreas) NearestArea(context.Context, uuid.UUID, geo.Point) (*ZoneRef, float64, error) {
	return f.near, f.km, nil
}

func busiaAreas() fakeAreas {
	alupe := geo.Point{Lat: 0.497094, Lng: 34.13416}
	return fakeAreas{
		cov: Coverage{
			Zones: []CoverageZone{
				{ID: uuid.New(), Name: "Alupe", Type: TypeDelivery, Center: &alupe, Aliases: []string{"Alupe Market"}},
				{ID: uuid.New(), Name: "No-go yard", Type: TypeExclusion, Center: &alupe},
			},
			Outlets: []OutletPoint{{ID: uuid.New(), Name: "Urban Loft Cafe Busia", Point: geo.Point{Lat: 0.4545662, Lng: 34.1272854}}},
			Bounds:  &[4]float64{34.0, 0.3, 34.3, 0.7},
		},
		near: &ZoneRef{ID: uuid.New(), Name: "Alupe"},
		km:   0,
	}
}

func TestGeocoderSearchPutsAreasAndOutletsFirst(t *testing.T) {
	var gotUA, gotViewbox string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotViewbox = r.URL.Query().Get("viewbox")
		_, _ = w.Write([]byte(`[{"lat":"0.4971","lon":"34.1342","name":"Alupe University","display_name":"Alupe University, Busia","type":"university"}]`))
	}))
	defer srv.Close()

	g := NewGeocoder(srv.URL, "codevertex-test/1.0", busiaAreas(), nil, zap.NewNop())
	got, err := g.Search(context.Background(), uuid.New(), "alupe", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "Alupe" || got[0].Source != "zone" || got[1].Name != "Alupe University" || got[1].Area == nil {
		t.Fatalf("results = %+v", got)
	}
	if gotUA != "codevertex-test/1.0" {
		t.Fatalf("user agent not sent: %q", gotUA)
	}
	if !strings.HasPrefix(gotViewbox, "33.85") {
		t.Fatalf("viewbox should pad the coverage bounds, got %q", gotViewbox)
	}

	// Alias and outlet matches; exclusion zones never appear as places.
	got, _ = g.Search(context.Background(), uuid.New(), "market", 5)
	if len(got) == 0 || got[0].Name != "Alupe" {
		t.Fatalf("alias match missing: %+v", got)
	}
	got, _ = g.Search(context.Background(), uuid.New(), "urban loft", 5)
	if len(got) == 0 || got[0].Kind != "outlet" {
		t.Fatalf("outlet match missing: %+v", got)
	}
	for _, p := range got {
		if p.Name == "No-go yard" {
			t.Fatal("exclusion zones must not be offered as places")
		}
	}
	if got, _ := g.Search(context.Background(), uuid.New(), "a", 5); len(got) != 0 {
		t.Fatal("one-letter queries return nothing")
	}
}

func TestGeocoderSearchSurvivesGeocoderOutage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	g := NewGeocoder(srv.URL, "ua", busiaAreas(), nil, zap.NewNop())
	got, err := g.Search(context.Background(), uuid.New(), "alupe", 5)
	if err != nil || len(got) != 1 || got[0].Name != "Alupe" {
		t.Fatalf("tenant areas should still be offered: %+v %v", got, err)
	}
}

func TestGeocoderReverse(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		_, _ = w.Write([]byte(`{"lat":"0.4971","lon":"34.1342","name":"","display_name":"Busia Road, Alupe, Busia","type":"road","address":{"road":"Busia Road","village":"Alupe"}}`))
	}))
	defer srv.Close()
	g := NewGeocoder(srv.URL, "ua", busiaAreas(), nil, zap.NewNop())

	pin := geo.Point{Lat: 0.49712, Lng: 34.13419}
	p, err := g.Reverse(context.Background(), uuid.New(), pin)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Busia Road" || p.Area == nil || p.Area.Name != "Alupe" {
		t.Fatalf("reverse = %+v", p)
	}
	if p.Location != pin {
		t.Fatal("reverse must keep the exact pin, not the geocoder's snapped point")
	}
	if _, err := g.Reverse(context.Background(), uuid.New(), geo.Point{}); err == nil {
		t.Fatal("0,0 must be rejected")
	}
}

func TestGeocoderReverseFallsBackToArea(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	g := NewGeocoder(srv.URL, "ua", busiaAreas(), nil, zap.NewNop())
	p, err := g.Reverse(context.Background(), uuid.New(), geo.Point{Lat: 0.5, Lng: 34.14})
	if err != nil || p.Name != "Near Alupe" {
		t.Fatalf("expected the area as the name when the geocoder is down: %+v %v", p, err)
	}
	none := NewGeocoder(srv.URL, "ua", fakeAreas{}, nil, zap.NewNop())
	p, _ = none.Reverse(context.Background(), uuid.New(), geo.Point{Lat: 0.5, Lng: 34.14})
	if p.Name != "0.50000, 34.14000" {
		t.Fatalf("expected coordinates as the last resort: %q", p.Name)
	}
}
