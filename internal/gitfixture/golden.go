package gitfixture

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden is read once at test-binary startup; set UPDATE_GOLDEN=1 to
// (re)write golden files instead of checking against them.
var updateGolden = os.Getenv("UPDATE_GOLDEN") == "1"

// Golden compares got against testdata/<name>.golden (relative to the calling
// package's directory, per Go's test working-directory convention), failing
// with a one-line pointer to the first differing line. Run with
// UPDATE_GOLDEN=1 to create or refresh the golden file.
func Golden(t testing.TB, name string, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	got = strings.TrimRight(got, "\n") + "\n"

	if updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("gitfixture: mkdir testdata: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("gitfixture: write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("gitfixture: golden file %s missing - run with UPDATE_GOLDEN=1 to create it (%v)", path, err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch for %s: %s", name, firstDiffLine(string(want), got))
	}
}

// firstDiffLine returns a compact, single-line description of the first line
// at which want and got diverge - the "one-line diffs on failure" the harness
// is required to produce (§28.2 rule 3), so a failing test is scannable
// without opening a diff tool.
func firstDiffLine(want, got string) string {
	wl := strings.Split(want, "\n")
	gl := strings.Split(got, "\n")
	n := len(wl)
	if len(gl) > n {
		n = len(gl)
	}
	for i := 0; i < n; i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("line %d: want %q, got %q", i+1, w, g)
		}
	}
	return fmt.Sprintf("line counts differ: want %d lines, got %d lines", len(wl), len(gl))
}
