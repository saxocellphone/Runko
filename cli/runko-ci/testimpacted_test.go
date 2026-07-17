package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
)

// writeFakeBin writes an executable shell script standing in for bazel or
// target-determinator (the repo's scripted-fake-engine pattern) and
// returns its path.
func writeFakeBin(t *testing.T, dir, name, body string) string {
	t.Helper()
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return bin
}

// testImpactedFixture builds a repo with a refinable root pattern
// (MODULE.bazel), a blunt one (Makefile), and one project a/ - the same
// §14.5.8 shape snapshot_test.go uses - plus fake bazel and determinator
// binaries whose invocations are recorded under binDir.
func testImpactedFixture(t *testing.T, determinatorBody, bazelBody string) (repo *gitfixture.Repo, base string, opts TestImpactedOptions, binDir string) {
	t.Helper()
	repo = gitfixture.New(t)
	repo.WriteFile("PROJECT.yaml", `schema: project/v1
name: repo
type: other
root_invalidation:
  - pattern: MODULE.bazel
    refinable: true
  - Makefile
ci:
  checks:
    - name: docs-check
      command: make check-docs
`)
	repo.WriteFile("a/PROJECT.yaml", checksManifest("a", "a-test", "bazel test //a/..."))
	repo.WriteFile("a/a.go", "package a\n")
	repo.WriteFile("MODULE.bazel", "module(name = \"x\")\n")
	base = repo.Commit("seed")

	binDir = t.TempDir()
	writeFakeBin(t, binDir, "target-determinator", "echo determinator-called >> "+filepath.Join(binDir, "determinator.log")+"\n"+determinatorBody)
	writeFakeBin(t, binDir, "bazel", "echo \"$@\" >> "+filepath.Join(binDir, "bazel.argv")+"\n"+bazelBody)

	opts = TestImpactedOptions{
		RepoDir:         repo.Dir,
		Base:            base,
		Head:            "HEAD",
		Universe:        "//a/...",
		BazelBin:        filepath.Join(binDir, "bazel"),
		DeterminatorBin: filepath.Join(binDir, "target-determinator"),
		BazelArgs:       []string{"--test_output=errors"},
	}
	return repo, base, opts, binDir
}

func bazelArgv(t *testing.T, binDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(binDir, "bazel.argv"))
	if err != nil {
		t.Fatalf("expected bazel to have been invoked: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func assertNotInvoked(t *testing.T, binDir, logName, who string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(binDir, logName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s not to be invoked (stat: %v)", who, err)
	}
}

// The core path: a plain source change, a successful diff - bazel test
// runs the manifest's args plus exactly the impacted targets, not the
// universe pattern.
func TestTestImpactedScopedRun(t *testing.T) {
	repo, _, opts, binDir := testImpactedFixture(t, `echo "//a:a_test"`, "")
	repo.WriteFile("a/a.go", "package a // edited\n")
	repo.Commit("edit a")

	code, err := TestImpacted(opts)
	if err != nil || code != 0 {
		t.Fatalf("TestImpacted: code=%d err=%v", code, err)
	}
	if got, want := bazelArgv(t, binDir), "test --test_output=errors //a:a_test"; got != want {
		t.Fatalf("bazel argv = %q, want %q", got, want)
	}
}

// An empty impacted set succeeds without invoking bazel at all - the
// whole point of scoping a closure-pulled lane whose targets the change
// never reaches.
func TestTestImpactedEmptyDiffSkipsBazel(t *testing.T) {
	repo, _, opts, binDir := testImpactedFixture(t, "true", "")
	repo.WriteFile("a/a.go", "package a // edited\n")
	repo.Commit("edit a")

	code, err := TestImpacted(opts)
	if err != nil || code != 0 {
		t.Fatalf("TestImpacted: code=%d err=%v", code, err)
	}
	assertNotInvoked(t, binDir, "bazel.argv", "bazel")
}

// No base revision (developer shell, post-land safety net): the full
// universe runs - the exact command the manifest declared - and the
// determinator is never consulted.
func TestTestImpactedNoBaseRunsFullUniverse(t *testing.T) {
	_, _, opts, binDir := testImpactedFixture(t, `echo "//a:a_test"`, "")
	opts.Base = ""

	code, err := TestImpacted(opts)
	if err != nil || code != 0 {
		t.Fatalf("TestImpacted: code=%d err=%v", code, err)
	}
	if got, want := bazelArgv(t, binDir), "test --test_output=errors //a/..."; got != want {
		t.Fatalf("bazel argv = %q, want %q", got, want)
	}
	assertNotInvoked(t, binDir, "determinator.log", "the determinator")
}

// A blunt root-invalidation match (Makefile) is out-of-graph: no diff can
// vouch for a narrower set, so the diff is not even attempted and the
// full universe runs (§14.5.8's all-or-nothing rule, mirrored here).
func TestTestImpactedBluntEscalationRunsFullUniverse(t *testing.T) {
	repo, _, opts, binDir := testImpactedFixture(t, `echo "//a:a_test"`, "")
	repo.WriteFile("Makefile", "check:\n\ttrue\n")
	repo.WriteFile("a/a.go", "package a // edited\n")
	repo.Commit("makefile + source")

	code, err := TestImpacted(opts)
	if err != nil || code != 0 {
		t.Fatalf("TestImpacted: code=%d err=%v", code, err)
	}
	if got, want := bazelArgv(t, binDir), "test --test_output=errors //a/..."; got != want {
		t.Fatalf("bazel argv = %q, want %q", got, want)
	}
	assertNotInvoked(t, binDir, "determinator.log", "the determinator")
}

// A refinable-only escalation (MODULE.bazel) is exactly what the snapshot
// diff exists for - the graph sees it, so the scoped run stands.
func TestTestImpactedRefinableEscalationScopes(t *testing.T) {
	repo, _, opts, binDir := testImpactedFixture(t, `echo "//a:a_test"`, "")
	repo.WriteFile("MODULE.bazel", "module(name = \"x\", version = \"1\")\n")
	repo.Commit("bump module")

	code, err := TestImpacted(opts)
	if err != nil || code != 0 {
		t.Fatalf("TestImpacted: code=%d err=%v", code, err)
	}
	if got, want := bazelArgv(t, binDir), "test --test_output=errors //a:a_test"; got != want {
		t.Fatalf("bazel argv = %q, want %q", got, want)
	}
}

// A failing determinator falls back to the full universe - fail closed,
// never "run nothing".
func TestTestImpactedDeterminatorFailureRunsFullUniverse(t *testing.T) {
	repo, _, opts, binDir := testImpactedFixture(t, "echo boom >&2\nexit 1", "")
	repo.WriteFile("a/a.go", "package a // edited\n")
	repo.Commit("edit a")

	code, err := TestImpacted(opts)
	if err != nil || code != 0 {
		t.Fatalf("TestImpacted: code=%d err=%v", code, err)
	}
	if got, want := bazelArgv(t, binDir), "test --test_output=errors //a/..."; got != want {
		t.Fatalf("bazel argv = %q, want %q", got, want)
	}
}

// A missing determinator binary (post-land ci.yml run-checks jobs,
// developer machines) means the full universe runs - the wrapped command
// degrades to exactly its unwrapped form.
func TestTestImpactedMissingDeterminatorRunsFullUniverse(t *testing.T) {
	repo, _, opts, binDir := testImpactedFixture(t, "true", "")
	repo.WriteFile("a/a.go", "package a // edited\n")
	repo.Commit("edit a")
	opts.DeterminatorBin = filepath.Join(binDir, "no-such-determinator")

	code, err := TestImpacted(opts)
	if err != nil || code != 0 {
		t.Fatalf("TestImpacted: code=%d err=%v", code, err)
	}
	if got, want := bazelArgv(t, binDir), "test --test_output=errors //a/..."; got != want {
		t.Fatalf("bazel argv = %q, want %q", got, want)
	}
}

// Scoped mode maps bazel exit 4 ("no test targets were found") to
// success: bazel built the impacted targets before noticing none are
// tests, which is a real verification, not a skip.
func TestTestImpactedScopedNoTestsExitCodeMapsToSuccess(t *testing.T) {
	repo, _, opts, _ := testImpactedFixture(t, `echo "//a:a_lib"`, "exit 4")
	repo.WriteFile("a/a.go", "package a // edited\n")
	repo.Commit("edit a")

	code, err := TestImpacted(opts)
	if err != nil || code != 0 {
		t.Fatalf("scoped exit 4 must map to success, got code=%d err=%v", code, err)
	}
}

// The full-universe path keeps bazel's raw exit semantics: a universe
// pattern matching no tests is a manifest bug, exactly what an unwrapped
// command would have failed on.
func TestTestImpactedFullUniverseKeepsExitFour(t *testing.T) {
	_, _, opts, _ := testImpactedFixture(t, "true", "exit 4")
	opts.Base = ""

	code, err := TestImpacted(opts)
	if err != nil || code != 4 {
		t.Fatalf("full-universe exit 4 must propagate, got code=%d err=%v", code, err)
	}
}

// Test failures propagate bazel's exit code verbatim.
func TestTestImpactedTestFailurePropagates(t *testing.T) {
	repo, _, opts, _ := testImpactedFixture(t, `echo "//a:a_test"`, "exit 3")
	repo.WriteFile("a/a.go", "package a // edited\n")
	repo.Commit("edit a")

	code, err := TestImpacted(opts)
	if err != nil || code != 3 {
		t.Fatalf("want bazel's exit 3 back, got code=%d err=%v", code, err)
	}
}

// The cmd layer: --universe is required, structured (§6.5).
func TestTestImpactedRequiresUniverse(t *testing.T) {
	err := cmdTestImpacted([]string{"--repo", t.TempDir()})
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "missing_field" || ce.Field != "--universe" {
		t.Fatalf("want a structured missing_field error for --universe, got %v", err)
	}
}

// The cmd layer resolves base and head from the §14.4 executor payload env
// (BASE_SHA/HEAD_SHA) when flags don't say otherwise.
func TestTestImpactedReadsExecutorEnv(t *testing.T) {
	repo, base, opts, binDir := testImpactedFixture(t, `echo "//a:a_test"`, "")
	repo.WriteFile("a/a.go", "package a // edited\n")
	head := repo.Commit("edit a")
	t.Setenv("BASE_SHA", base)
	t.Setenv("HEAD_SHA", head)

	err := cmdTestImpacted([]string{
		"--repo", repo.Dir,
		"--universe", "//a/...",
		"--bazel-bin", opts.BazelBin,
		"--determinator-bin", opts.DeterminatorBin,
		"--", "--test_output=errors",
	})
	if err != nil {
		t.Fatalf("cmdTestImpacted: %v", err)
	}
	if got, want := bazelArgv(t, binDir), "test --test_output=errors //a:a_test"; got != want {
		t.Fatalf("bazel argv = %q, want %q", got, want)
	}
}
