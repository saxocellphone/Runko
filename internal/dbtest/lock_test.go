package dbtest

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// TestConnectSerializesConcurrentTests pins the harness's self-serialization
// (the advisory lock replacing -p 1 / --local_test_jobs=1): two tests
// holding Connect at the same time must never overlap - the second blocks
// until the first's cleanup releases the session lock. Parallel subtests
// stand in for the concurrent test processes a parallel bazel/go run
// produces; the lock is session-level in Postgres, so what serializes two
// goroutines' sessions here serializes two OS processes' sessions
// identically.
//
// "Postgres" in the name is load-bearing: check-db and check-bazel-db run
// `-run Postgres` / `--test_filter=Postgres`, and this test's first
// version, named without it, never executed in those lanes - it passed
// only under the unfiltered internal-test check.
func TestConnectSerializesConcurrentTestsAgainstLivePostgres(t *testing.T) {
	if os.Getenv(envVar) == "" {
		t.Skipf("%s not set - skipping live-Postgres test", envVar)
	}

	var active, overlaps int64
	t.Run("group", func(t *testing.T) {
		for _, name := range []string{"first", "second"} {
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				Connect(t)
				if atomic.AddInt64(&active, 1) > 1 {
					atomic.AddInt64(&overlaps, 1)
				}
				// Dwell long enough that an unserialized second Connect
				// would land inside this window. The holder count drops
				// before the lock releases (Cleanup runs after return),
				// so a serialized second holder can never see active > 1.
				time.Sleep(300 * time.Millisecond)
				atomic.AddInt64(&active, -1)
			})
		}
	})
	// t.Run only returns once both parallel subtests (and their cleanups)
	// have completed, so the counters are final here.
	if overlaps > 0 {
		t.Fatalf("expected Connect to serialize concurrent tests, but %d saw another holder active", overlaps)
	}
}
