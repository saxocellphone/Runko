package dbtest

import (
	"strings"
	"testing"
)

// The embedded-FS enumeration is the piece resetSchema depends on that CAN
// run without a live database: every up has a down, ordering is
// ascending/descending, and nothing is empty. (The full reset path runs
// under check-bazel-db / check-db against real Postgres.)
func TestMigrationInventory(t *testing.T) {
	ups, downs, err := migrationNames()
	if err != nil {
		t.Fatalf("migrationNames: %v", err)
	}
	if len(ups) == 0 || len(ups) != len(downs) {
		t.Fatalf("expected paired migrations, got %d ups / %d downs", len(ups), len(downs))
	}
	for i := 1; i < len(ups); i++ {
		if ups[i-1] >= ups[i] {
			t.Fatalf("ups must ascend: %s >= %s", ups[i-1], ups[i])
		}
	}
	for i := 1; i < len(downs); i++ {
		if downs[i-1] <= downs[i] {
			t.Fatalf("downs must descend: %s <= %s", downs[i-1], downs[i])
		}
	}
	for _, up := range ups {
		if b := mustRead(t, up); len(b) == 0 || !strings.Contains(up, ".up.sql") {
			t.Fatalf("suspicious migration %s (%d bytes)", up, len(b))
		}
	}
}
