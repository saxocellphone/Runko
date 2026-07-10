package bazel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/platform/buildadapter"
)

// scriptedBazel writes an executable shell script standing in for `bazel`,
// so these tests exercise the real exec.Command/argv-quoting/stdout-parsing
// path without needing a real Bazel install (unavailable in this sandbox -
// see CLAUDE.md). body receives argv (assembled by the script itself into
// $@) and must be valid POSIX shell.
func scriptedBazel(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-bazel")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bazel script: %v", err)
	}
	return path
}

func TestQueryParsesLabelOutput(t *testing.T) {
	bin := scriptedBazel(t, `echo "//commerce/checkout:go_default_test"
echo "//commerce/checkout/internal:helpers_test"
`)
	e := Engine{Bin: bin}
	result, err := e.Query(context.Background(), buildQueryRequest(t, "commerce/checkout/main.go"))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %v", result.Targets)
	}
}

func TestQueryFailsOnNonZeroExit(t *testing.T) {
	bin := scriptedBazel(t, `echo "ERROR: no targets found beneath ''" >&2
exit 1
`)
	e := Engine{Bin: bin}
	_, err := e.Query(context.Background(), buildQueryRequest(t, "commerce/checkout/main.go"))
	if err == nil {
		t.Fatalf("expected an error on non-zero exit")
	}
	if !strings.Contains(err.Error(), "no targets found") {
		t.Fatalf("expected bazel's stderr in the error, got %v", err)
	}
}

func TestQueryFailsOnTimeout(t *testing.T) {
	bin := scriptedBazel(t, `sleep 5
echo "//should:not-appear"
`)
	e := Engine{Bin: bin}
	req := buildQueryRequest(t, "commerce/checkout/main.go")
	req.Timeout = 50 * time.Millisecond

	start := time.Now()
	_, err := e.Query(context.Background(), req)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected an error on timeout")
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("expected Query to respect the timeout instead of waiting for the full sleep, took %v", elapsed)
	}
}

func TestQuerySkipsInvocationWhenNoChangedPaths(t *testing.T) {
	// Point at a binary that doesn't exist - if Query tried to invoke it,
	// this would fail with "executable file not found".
	e := Engine{Bin: filepath.Join(t.TempDir(), "does-not-exist")}
	result, err := e.Query(context.Background(), buildadapter.QueryRequest{})
	if err != nil {
		t.Fatalf("expected no invocation (and no error) with zero changed paths, got %v", err)
	}
	if len(result.Targets) != 0 {
		t.Fatalf("expected no targets, got %v", result.Targets)
	}
}

func TestFileLabelConversion(t *testing.T) {
	cases := map[string]string{
		"commerce/checkout/main.go": "//commerce/checkout:main.go",
		"go.mod":                    "//:go.mod",
		"a/b/c/d.go":                "//a/b/c:d.go",
	}
	for input, want := range cases {
		if got := fileLabel(input); got != want {
			t.Fatalf("fileLabel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestQueryUsesRdepsRecipeWithConvertedLabels(t *testing.T) {
	// The script records its own argv so the test can assert the exact
	// query string Query() constructed, matching docs/spec/build-adapter/
	// README.md §5's recipe precisely (not just "some query ran").
	recorded := filepath.Join(t.TempDir(), "argv.txt")
	bin := scriptedBazel(t, `printf '%s\n' "$*" > `+recorded+`
`)
	e := Engine{Bin: bin}
	req := buildQueryRequest(t, "commerce/checkout/main.go")
	req.UniversePattern = "//commerce/..."

	if _, err := e.Query(context.Background(), req); err != nil {
		t.Fatalf("Query: %v", err)
	}

	got, err := os.ReadFile(recorded)
	if err != nil {
		t.Fatalf("read recorded argv: %v", err)
	}
	argv := strings.TrimSpace(string(got))
	if !strings.Contains(argv, "query") {
		t.Fatalf("expected the 'query' subcommand, got argv: %q", argv)
	}
	if !strings.Contains(argv, "rdeps(//commerce/..., set(//commerce/checkout:main.go))") {
		t.Fatalf("expected the rdeps recipe with the converted file label, got argv: %q", argv)
	}
	if !strings.Contains(argv, "--output=label") {
		t.Fatalf("expected --output=label, got argv: %q", argv)
	}
}

func buildQueryRequest(t *testing.T, changedPath string) buildadapter.QueryRequest {
	t.Helper()
	// The changed path must live inside a bazel PACKAGE within RepoDir:
	// Query skips non-package paths (migration-findings #6). An unset
	// RepoDir resolved BUILD lookups against the test process cwd, which
	// under plain `go test` is the real package dir - so these fixtures
	// accidentally found the REAL repo's BUILD files and passed, while
	// bazel's runfiles sandbox (no BUILD files) failed deterministically.
	dir := t.TempDir()
	pkg := filepath.Join(dir, filepath.Dir(changedPath))
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "BUILD.bazel"), []byte("filegroup(name = \"x\")\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return buildadapter.QueryRequest{RepoDir: dir, ChangedPaths: []string{changedPath}}
}

// Package-aware filtering (migration-findings #6): non-package paths are
// skipped instead of erroring the whole query, and an all-non-package
// change reports no targets without invoking the engine at all.
func TestQuerySkipsPathsOutsideAnyBazelPackage(t *testing.T) {
	dir := t.TempDir()
	// A package dir with a BUILD file, and root content without one.
	if err := os.MkdirAll(filepath.Join(dir, "svc"), 0o755); err != nil {
		t.Fatal(err)
	}
	for f, c := range map[string]string{
		"svc/BUILD.bazel": "filegroup(name='x')",
		"svc/main.go":     "package main",
		"README.md":       "hi",
	} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// All-non-package change: no engine call needed - the scripted fake
	// would fail loudly if invoked (it exits 1).
	failing := scriptedBazel(t, "#!/bin/sh\nexit 1\n")
	res, err := Engine{Bin: failing}.Query(context.Background(), buildadapter.QueryRequest{
		RepoDir: dir, UniversePattern: "//...", ChangedPaths: []string{"README.md"},
	})
	if err != nil || len(res.Targets) != 0 {
		t.Fatalf("all-non-package change must report no targets without querying, got %v / %v", res.Targets, err)
	}

	// Mixed change: only the in-package label reaches the query.
	echoing := scriptedBazel(t, "#!/bin/sh\necho \"$@\" >&2\necho //svc:svc_test\n")
	res, err = Engine{Bin: echoing}.Query(context.Background(), buildadapter.QueryRequest{
		RepoDir: dir, UniversePattern: "//...", ChangedPaths: []string{"README.md", "svc/main.go"},
	})
	if err != nil || len(res.Targets) != 1 {
		t.Fatalf("mixed change must query the package path only, got %v / %v", res.Targets, err)
	}
}
