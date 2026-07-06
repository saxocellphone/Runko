package gitfixture

import "time"

// epoch is the fixed start time for every FakeClock, chosen arbitrarily but
// deterministically so golden files never depend on wall-clock time.
var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// FakeClock is a deterministic, monotonically-advancing clock for tests
// (docs/design.md §28.2 rule 3: "fake clock + seeded IDs" - a flaky test from
// wall-clock or random-ID nondeterminism is the worst token multiplier there is).
type FakeClock struct {
	now time.Time
}

// NewFakeClock returns a clock starting at a fixed epoch.
func NewFakeClock() *FakeClock {
	return &FakeClock{now: epoch}
}

// Now returns the current fake time.
func (c *FakeClock) Now() time.Time {
	return c.now
}

// Advance moves the clock forward by d and returns the new time. Each fixture
// commit advances the clock by one tick so successive commits get distinct,
// reproducible timestamps.
func (c *FakeClock) Advance(d time.Duration) time.Time {
	c.now = c.now.Add(d)
	return c.now
}
