package checks

import "time"

// IsStale reports whether a non-terminal CheckRun has exceeded its TTL
// without a fresh update (§14.4.2): "a dead CI must block loudly, not hang
// silently." A completed run is never stale regardless of age.
func IsStale(status CheckStatus, lastSeenAt time.Time, ttlSeconds int, now time.Time) bool {
	if status == CheckStatusCompleted {
		return false
	}
	if ttlSeconds <= 0 {
		return false
	}
	return now.Sub(lastSeenAt) > time.Duration(ttlSeconds)*time.Second
}

// DefaultTTLSeconds is §14.4.2's default TTL for required runs in
// queued/in_progress: 24 hours.
const DefaultTTLSeconds = 24 * 60 * 60
