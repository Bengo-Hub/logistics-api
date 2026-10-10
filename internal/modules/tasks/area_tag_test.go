package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type fakeTagger struct {
	calls                  int
	zoneID, zoneName       string
	km                     float64
	err                    error
	gotPickLat, gotDropLat float64
	gotOutlet              uuid.UUID
}

func (f *fakeTagger) TagTaskDropoff(_ context.Context, _, outletID uuid.UUID, pickLat, _, dropLat, _ float64) (string, string, float64, error) {
	f.calls++
	f.gotPickLat, f.gotDropLat, f.gotOutlet = pickLat, dropLat, outletID
	return f.zoneID, f.zoneName, f.km, f.err
}

func newTaggingService(t *fakeTagger) *Service {
	s := NewService(nil, zap.NewNop())
	s.SetAreaTagger(t)
	return s
}

func TestTagDropoffAreaFromRequestCoords(t *testing.T) {
	tg := &fakeTagger{zoneID: "z1", zoneName: "Bugengi", km: 4.2}
	outlet := uuid.New()
	meta := newTaggingService(tg).tagDropoffArea(context.Background(), uuid.New(), CreateTaskRequest{
		DropoffLat: 0.47, DropoffLng: 34.16, PickupLat: 0.45, PickupLng: 34.12,
		Metadata: map[string]any{"outlet_id": outlet.String()},
	})
	if meta["zone_id"] != "z1" || meta["zone_name"] != "Bugengi" || meta["distance_km"] != 4.2 {
		t.Fatalf("metadata = %v", meta)
	}
	if tg.gotPickLat != 0.45 || tg.gotDropLat != 0.47 || tg.gotOutlet != outlet {
		t.Fatalf("tagger got pick %v drop %v outlet %v", tg.gotPickLat, tg.gotDropLat, tg.gotOutlet)
	}
}

func TestTagDropoffAreaFromMetadataCoords(t *testing.T) {
	tg := &fakeTagger{zoneID: "z1", zoneName: "Alupe", km: 7.25}
	meta := newTaggingService(tg).tagDropoffArea(context.Background(), uuid.New(), CreateTaskRequest{
		Metadata: map[string]any{"dropoff_lat": 0.497, "dropoff_lng": 34.134, "pickup_lat": 0.4545, "pickup_lng": 34.127},
	})
	if meta["zone_name"] != "Alupe" || tg.gotDropLat != 0.497 || tg.gotPickLat != 0.4545 {
		t.Fatalf("metadata coords not used: %v", meta)
	}
}

func TestTagDropoffAreaTrustsUpstreamQuote(t *testing.T) {
	tg := &fakeTagger{zoneID: "other"}
	meta := newTaggingService(tg).tagDropoffArea(context.Background(), uuid.New(), CreateTaskRequest{
		DropoffLat: 1, DropoffLng: 34,
		Metadata: map[string]any{"zone_id": "q1", "zone_name": "Malaba", "distance_km": 31.4},
	})
	if meta != nil || tg.calls != 0 {
		t.Fatalf("an upstream quote must be kept as is: %v (calls %d)", meta, tg.calls)
	}
}

func TestTagDropoffAreaPerKmKeepsDistanceOnly(t *testing.T) {
	tg := &fakeTagger{km: 10.2} // outside every zone
	meta := newTaggingService(tg).tagDropoffArea(context.Background(), uuid.New(), CreateTaskRequest{DropoffLat: 0.52, DropoffLng: 34.13})
	if _, ok := meta["zone_id"]; ok || meta["distance_km"] != 10.2 {
		t.Fatalf("per-km drop-off should record distance only: %v", meta)
	}
}

func TestTagDropoffAreaSkips(t *testing.T) {
	tg := &fakeTagger{err: errors.New("boom")}
	s := newTaggingService(tg)
	if m := s.tagDropoffArea(context.Background(), uuid.New(), CreateTaskRequest{}); m != nil || tg.calls != 0 {
		t.Fatal("no drop-off coordinates: nothing to tag")
	}
	if m := s.tagDropoffArea(context.Background(), uuid.New(), CreateTaskRequest{DropoffLat: 1, DropoffLng: 34}); m != nil {
		t.Fatal("tagger errors must not change the task")
	}
	if m := NewService(nil, zap.NewNop()).tagDropoffArea(context.Background(), uuid.New(), CreateTaskRequest{DropoffLat: 1, DropoffLng: 34}); m != nil {
		t.Fatal("without a tagger nothing changes")
	}
}
