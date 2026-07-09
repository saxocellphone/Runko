//go:build bazel_integration

// This file only builds with `go test -tags bazel_integration`. It requires
// a real `bazel` binary on PATH; it exists so a real Bazel install (a dev
// machine, the CI bazel job) can verify the rdeps recipe against the genuine
// engine, not just the scripted fake in bazel_test.go. The fixture is an
// empty MODULE.bazel (bzlmod workspace boundary - Bazel 8 dropped WORKSPACE
// evaluation) plus the repo's own .bazelversion copied in, since bazelisk
// resolves the version from the FIXTURE's cwd and would otherwise download
// "latest" and break hermeticity.
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
	mustWrite(t, filepath.Join(dir, "MODULE.bazel"), "")
	if pin, err := os.ReadFile("../../.bazelversion"); err == nil {
		mustWrite(t, filepath.Join(dir, ".bazelversion"), string(pin))
	}
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
