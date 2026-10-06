package telemetry

import (
	"testing"
	"time"
)

func TestRetentionCutoff(t *testing.T) {
	now := time.Date(2026, 10, 6, 3, 0, 0, 0, time.UTC)
	if got := retentionCutoff(now, 30); !got.Equal(time.Date(2026, 9, 6, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("30 days: %v", got)
	}
	if got := retentionCutoff(now, 0); !got.Equal(retentionCutoff(now, defaultRetentionDays)) {
		t.Fatalf("zero days must fall back to the default, got %v", got)
	}
}

func TestParseRetentionDays(t *testing.T) {
	for in, want := range map[string]int{"14": 14, " 90 ": 90, "": defaultRetentionDays, "abc": defaultRetentionDays, "-3": defaultRetentionDays} {
		if got := parseRetentionDays(in); got != want {
			t.Fatalf("parseRetentionDays(%q) = %d, want %d", in, got, want)
		}
	}
}
