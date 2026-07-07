//go:build bazel_integration

// This file only builds with `go test -tags bazel_integration`. It requires
// a real `bazel` binary and a minimal WORKSPACE/BUILD fixture, neither of
// which exist in this sandbox (no Bazel install here - see CLAUDE.md); it
// exists so a real Bazel install (a dev machine, CI with the tag enabled)
// can verify the rdeps recipe against the genuine engine, not just the
// scripted fake in bazel_test.go.
package bazel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/saxocellphone/runko/buildadapter"
)

func TestQueryAgainstRealBazel(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "WORKSPACE"), "")
	mustWrite(t, filepath.Join(dir, "commerce", "checkout", "BUILD"), `
filegroup(name = "srcs", srcs = ["main.go"])
`)
	mustWrite(t, filepath.Join(dir, "commerce", "checkout", "main.go"), "package main\n")
	mustWrite(t, filepath.Join(dir, "libs", "billing", "BUILD"), `
filegroup(name = "srcs", srcs = ["lib.go"])
`)
	mustWrite(t, filepath.Join(dir, "libs", "billing", "lib.go"), "package billing\n")

	e := Engine{}
	result, err := e.Query(context.Background(), buildadapter.QueryRequest{
		RepoDir:      dir,
		ChangedPaths: []string{"commerce/checkout/main.go"},
	})
	if err != nil {
		t.Fatalf("Query against a real bazel: %v", err)
	}
	if len(result.Targets) == 0 {
		t.Fatalf("expected at least the srcs filegroup depending on main.go, got none")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
