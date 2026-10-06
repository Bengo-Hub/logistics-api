package dispatch

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFreshCandidates(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	online, stale, declined, unknown := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	cands := []riderCandidate{
		{MemberID: online, DistanceKm: 2},
		{MemberID: stale, DistanceKm: 1},
		{MemberID: declined, DistanceKm: 0.5},
		{MemberID: unknown, DistanceKm: 0.2},
	}
	seen := map[string]time.Time{
		online.String():   now.Add(-2 * time.Minute),
		stale.String():    now.Add(-26 * time.Hour), // switched the app off yesterday
		declined.String(): now.Add(-1 * time.Minute),
	}
	got := freshCandidates(cands, seen, map[uuid.UUID]bool{declined: true}, now, maxFixAge)
	if len(got) != 1 || got[0].MemberID != online {
		t.Fatalf("got %v, want only the online rider", got)
	}
}

func TestFreshCandidatesKeepsOrderAndNilExclude(t *testing.T) {
	now := time.Now()
	a, b := uuid.New(), uuid.New()
	seen := map[string]time.Time{a.String(): now, b.String(): now}
	got := freshCandidates([]riderCandidate{{MemberID: a}, {MemberID: b}}, seen, nil, now, maxFixAge)
	if len(got) != 2 || got[0].MemberID != a {
		t.Fatalf("got %v", got)
	}
}
