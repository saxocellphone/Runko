package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

// fakeDeterminatorOnPath installs an executable named exactly
// target-determinator at the front of PATH - the zero-value bazel engine
// resolves it there, so Checks' snapshot-diff path runs for real against a
// real git clone with only the third-party binary faked.
func fakeDeterminatorOnPath(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "target-determinator")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("write fake determinator: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func snapshotChecksFixture(t *testing.T) (repo *gitfixture.Repo, base string) {
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
	repo.WriteFile("b/PROJECT.yaml", "schema: project/v1\nname: b\ntype: service\ndependencies:\n  - a\nci:\n  checks:\n    - name: b-test\n      command: bazel test //b/...\n")
	repo.WriteFile("MODULE.bazel", "module(name = \"x\")\n")
	base = repo.Commit("seed")
	return repo, base
}

// §14.5.8 end to end: a MODULE.bazel-only change escalates the floor, the
// snapshot diff succeeds, and the matrix narrows to the diff-impacted
// project (a), its declared dependents (b), and the de-escalated path's
// owner (root -> docs-check) - instead of every project's checks.
func TestChecksSnapshotDiffNarrowsRefinableEscalation(t *testing.T) {
	repo, base := snapshotChecksFixture(t)
	repo.WriteFile("MODULE.bazel", "module(name = \"x\", version = \"1\")\n")
	head := repo.Commit("bump module")
	fakeDeterminatorOnPath(t, `echo "//a:a_test"`)

	out, err := Checks(repo.Dir, base, head, nil, "bazel", "", 0)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if out.RunEverything {
		t.Fatalf("successful snapshot diff must narrow, got %+v", out)
	}
	var names []string
	for _, c := range out.Checks {
		names = append(names, c.Name)
	}
	want := []string{"a-test", "b-test", "docs-check"}
	if len(names) != len(want) {
		t.Fatalf("want %v, got %v", want, names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("want %v, got %v", want, names)
		}
	}
	if out.BuildRefinement == nil || out.BuildRefinement.Strategy != "snapshot_diff" || out.BuildRefinement.RunEverything {
		t.Fatalf("want a successful snapshot_diff refinement in the audit trail, got %+v", out.BuildRefinement)
	}
}

// A failing determinator keeps run_everything - fail closed, with the
// failure recorded in the audit trail.
func TestChecksSnapshotDiffFailureKeepsRunEverything(t *testing.T) {
	repo, base := snapshotChecksFixture(t)
	repo.WriteFile("MODULE.bazel", "module(name = \"x\", version = \"2\")\n")
	head := repo.Commit("bump module")
	fakeDeterminatorOnPath(t, `echo "boom" >&2
exit 1`)

	out, err := Checks(repo.Dir, base, head, nil, "bazel", "", 0)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if !out.RunEverything {
		t.Fatalf("failed diff must stay run_everything: %+v", out)
	}
	if len(out.Checks) != 3 { // every project's checks: docs-check, a-test, b-test
		t.Fatalf("run_everything must resolve every check, got %v", out.Checks)
	}
	if out.BuildRefinement == nil || !out.BuildRefinement.RunEverything {
		t.Fatalf("failure must be visible in the audit trail, got %+v", out.BuildRefinement)
	}
}

// A blunt pattern in the same change means the diff is never attempted:
// refinability is all-or-nothing per change (§14.5.8).
func TestChecksSnapshotDiffNotAttemptedOnMixedEscalation(t *testing.T) {
	repo, base := snapshotChecksFixture(t)
	repo.WriteFile("MODULE.bazel", "module(name = \"x\", version = \"3\")\n")
	repo.WriteFile("Makefile", "check:\n\ttrue\n")
	head := repo.Commit("module + makefile")
	// A fake that would narrow if consulted - proving it wasn't.
	fakeDeterminatorOnPath(t, `echo "//a:a_test"`)

	out, err := Checks(repo.Dir, base, head, nil, "bazel", "", 0)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if !out.RunEverything {
		t.Fatalf("mixed escalation must stay run_everything: %+v", out)
	}
	if out.BuildRefinement != nil && out.BuildRefinement.Strategy == "snapshot_diff" {
		t.Fatalf("snapshot diff must not be attempted on mixed escalation, got %+v", out.BuildRefinement)
	}
}

// An empty diff narrows to just the de-escalated owner attribution: a
// MODULE.bazel edit that impacts zero targets needs only the root
// project's checks.
func TestChecksSnapshotDiffEmptyDiffNarrowsToOwner(t *testing.T) {
	repo, base := snapshotChecksFixture(t)
	repo.WriteFile("MODULE.bazel", "module(name = \"x\", version = \"4\")\n")
	head := repo.Commit("comment-only bump")
	fakeDeterminatorOnPath(t, `true`)

	out, err := Checks(repo.Dir, base, head, nil, "bazel", "", 0)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if out.RunEverything {
		t.Fatalf("empty diff must narrow: %+v", out)
	}
	if len(out.Checks) != 1 || out.Checks[0].Name != "docs-check" {
		t.Fatalf("want just the owner's docs-check, got %v", out.Checks)
	}
}

// §14.5.9 through the snapshot path: a de-escalated result must keep the
// direct/closure distinction. A mixed change (refinable MODULE.bazel + a's
// own code) leaves b closure-affected only - b's run_when:direct lane must
// NOT ride the narrowed matrix, while its affected-class check still does.
func TestChecksSnapshotDiffKeepsClosureMembersNonDirect(t *testing.T) {
	repo, _ := snapshotChecksFixture(t)
	// b gains a direct-only unit lane beside its affected-class check -
	// IN THE BASE: touching b's manifest in the head commit would make b
	// genuinely direct and defeat the scenario.
	repo.WriteFile("b/PROJECT.yaml", `schema: project/v1
name: b
type: service
dependencies:
  - a
ci:
  checks:
    - name: b-test
      command: bazel test //b/...
    - name: b-race
      command: bazel test --race //b/...
      run_when: direct
`)
	base := repo.Commit("b gains a direct-only lane")
	repo.WriteFile("MODULE.bazel", "module(name = \"x\", version = \"6\")\n")
	repo.WriteFile("a/main.go", "package a\n")
	head := repo.Commit("module bump + a code")
	fakeDeterminatorOnPath(t, `true`) // empty diff: the module bump impacts nothing

	out, err := Checks(repo.Dir, base, head, nil, "bazel", "", 0)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if out.RunEverything {
		t.Fatalf("mixed refinable+code change with a clean diff must narrow: %+v", out)
	}
	var names []string
	for _, c := range out.Checks {
		names = append(names, c.Name)
	}
	want := []string{"a-test", "b-test", "docs-check"} // b-race must be absent: b is closure-only
	if len(names) != len(want) {
		t.Fatalf("want %v, got %v", want, names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("want %v, got %v", want, names)
		}
	}
}

// Without --engine nothing changes: the refinable marking alone never
// narrows anything (the gate and every engine-less caller see pure floor
// semantics).
func TestChecksWithoutEngineIgnoresRefinable(t *testing.T) {
	repo, base := snapshotChecksFixture(t)
	repo.WriteFile("MODULE.bazel", "module(name = \"x\", version = \"5\")\n")
	head := repo.Commit("bump module")

	out, err := Checks(repo.Dir, base, head, nil, "", "", 0)
	if err != nil {
		t.Fatalf("Checks: %v", err)
	}
	if !out.RunEverything || len(out.Checks) != 3 {
		t.Fatalf("floor semantics must be unchanged without an engine: %+v", out)
	}
}
