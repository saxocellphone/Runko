package checks

import (
	"testing"
	"time"
)

func TestIsStale(t *testing.T) {
	now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		status     CheckStatus
		lastSeen   time.Time
		ttlSeconds int
		want       bool
	}{
		{"completed never stale even if old", CheckStatusCompleted, now.Add(-48 * time.Hour), DefaultTTLSeconds, false},
		{"in_progress within TTL", CheckStatusInProgress, now.Add(-1 * time.Hour), DefaultTTLSeconds, false},
		{"in_progress past TTL", CheckStatusInProgress, now.Add(-25 * time.Hour), DefaultTTLSeconds, true},
		{"queued past TTL", CheckStatusQueued, now.Add(-25 * time.Hour), DefaultTTLSeconds, true},
		{"zero TTL never stale", CheckStatusInProgress, now.Add(-1000 * time.Hour), 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsStale(tc.status, tc.lastSeen, tc.ttlSeconds, now)
			if got != tc.want {
				t.Errorf("IsStale(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
