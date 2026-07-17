package main

// jj-client tests (§7.4's jj-first direction, 2026-07-08). Gated on a jj
// binary being present - CI runners don't carry jj (yet), so these skip
// there and run for real wherever jj is installed (the
// RUNKO_TEST_DATABASE_URL convention: skip, never fail, when the
// environment lacks the dependency).

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
	"github.com/saxocellphone/runko/runkod"
)

// zeroOIDForTest mirrors git's all-zeros old-sha convention for a
// brand-new ref (runkod's own zeroOID is unexported).
const zeroOIDForTest = "0000000000000000000000000000000000000000"

func writeTestFile(t *testing.T, dir, path, content string) {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireJJ(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("jj"); err != nil {
		t.Skip("jj binary not installed; skipping jj client tests")
	}
	// Hermetic HOME: jj resolves its "secure config" under the user's
	// config dir, which is read-only inside a bazel test sandbox and
	// pollutes results with the developer's real jj config under plain
	// `go test`. Point both at a throwaway dir.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
}

// newColocatedJJRepo initializes a colocated jj workspace (jj + .git side
// by side - the supported jj mode, since the daemon's transport and the
// provenance config are plain git).
func newColocatedJJRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("jj", "git", "init", "--colocate", dir).CombinedOutput(); err != nil {
		t.Fatalf("jj git init: %v: %s", err, out)
	}
	for _, kv := range [][2]string{{"user.name", "Test"}, {"user.email", "test@runko.dev"}} {
		if _, err := runJJ(dir, "config", "set", "--repo", kv[0], kv[1]); err != nil {
			t.Fatalf("jj config set %s: %v", kv[0], err)
		}
	}
	return dir
}

func jjCommitFile(t *testing.T, dir, path, content, message string) {
	t.Helper()
	writeTestFile(t, dir, path, content)
	if _, err := runJJ(dir, "commit", "-m", message); err != nil {
		t.Fatalf("jj commit: %v", err)
	}
}

func TestPushChangeFromColocatedJJWorkspace(t *testing.T) {
	requireJJ(t)
	remote := newBareRemote(t)
	dir := newColocatedJJRepo(t)
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}

	// Seed trunk through plain git (the daemon does this server-side in
	// production; here the bare remote just needs a main).
	jjCommitFile(t, dir, "README.md", "hi\n", "initial")
	seed, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatalf("resolve seed commit: %v", err)
	}
	if _, err := runGit(dir, "push", remote, seed+":refs/heads/main"); err != nil {
		t.Fatalf("seed trunk: %v", err)
	}

	// A two-Change stack, both trailers derived from jj change ids.
	jjCommitFile(t, dir, "proj/a.txt", "a\n", "change A")
	jjCommitFile(t, dir, "proj/b.txt", "b\n", "change B")

	id, err := PushChange(dir, remote, "main")
	if err != nil {
		t.Fatalf("PushChange from jj workspace: %v", err)
	}
	if !strings.HasPrefix(id, "I") || len(id) != 41 {
		t.Fatalf("expected a derived Change-Id, got %q", id)
	}

	tip, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatalf("resolve tip: %v", err)
	}
	pushed, err := runGit(remote, "rev-parse", "refs/for/main")
	if err != nil || pushed != tip {
		t.Fatalf("magic ref: want jj tip %s (NOT the empty @ working copy), got %s (%v)", tip, pushed, err)
	}

	// The id PushChange reports is the tip commit's trailer, verbatim.
	msg, _ := runGit(dir, "log", "-1", "--format=%B", tip)
	if trailerID, ok := receive.ParseChangeID(msg); !ok || trailerID != id {
		t.Fatalf("reported id %q vs tip trailer %q (ok=%v)", id, trailerID, ok)
	}
}

// The jj half of the push-anyway rule (2026-07-17): a conflicting
// auto-sync rolls the repo back to the pre-sync operation - jj records
// conflicts in-tree, and those markers must never reach the pushed
// commit - then the stale base is submitted with a warning. Conflicts
// gate landing, not review.
func TestPushChangeJJConflictingSyncRollsBackAndPushes(t *testing.T) {
	requireJJ(t)
	remote := newBareRemote(t)
	dir := newColocatedJJRepo(t)
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}
	// A NAMED remote, as workspace attach configures in production: the
	// sync's `git fetch origin main` then updates refs/remotes/origin/main,
	// which jj imports - a bare URL leaves the tip in FETCH_HEAD only,
	// invisible to jj.
	if _, err := runGit(dir, "remote", "add", "origin", remote); err != nil {
		t.Fatalf("remote add: %v", err)
	}

	jjCommitFile(t, dir, "shared.txt", "base\n", "initial")
	seed, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatalf("resolve seed commit: %v", err)
	}
	if _, err := runGit(dir, "push", "origin", seed+":refs/heads/main"); err != nil {
		t.Fatalf("seed trunk: %v", err)
	}

	// Local work touches shared.txt...
	jjCommitFile(t, dir, "shared.txt", "local line\n", "local work")

	// ...and trunk advances behind our back with a conflicting edit.
	other := gitfixture.New(t)
	configureIdentity(t, other.Dir)
	other.Run("remote add origin " + remote)
	if _, err := runGit(other.Dir, "fetch", "-q", "origin", "main"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, err := runGit(other.Dir, "reset", "-q", "--hard", "FETCH_HEAD"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	other.WriteFile("shared.txt", "trunk line\n")
	other.Commit("trunk advances")
	if _, err := runGit(other.Dir, "push", "-q", "origin", "main"); err != nil {
		t.Fatalf("advance remote main: %v", err)
	}

	var warnings strings.Builder
	oldWarn := warnWriter
	warnWriter = &warnings
	defer func() { warnWriter = oldWarn }()

	if _, err := PushChange(dir, "origin", "main"); err != nil {
		t.Fatalf("PushChange with a conflicting stale base: %v", err)
	}

	tip, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatalf("resolve tip: %v", err)
	}
	pushed, err := runGit(remote, "rev-parse", "refs/for/main")
	if err != nil || pushed != tip {
		t.Fatalf("magic ref: want the stale-base jj tip %s, got %s (%v)", tip, pushed, err)
	}
	// The rollback left no jj conflicts, and the pushed tree carries no
	// conflict markers.
	if out, _ := runJJ(dir, "log", "--no-graph", "-r", "conflicts() & mutable()", "-T", `change_id.short() ++ " "`); strings.TrimSpace(out) != "" {
		t.Fatalf("expected the conflicting rebase rolled back, but conflicts remain in: %s", out)
	}
	if blob, _ := runGit(dir, "show", pushed+":shared.txt"); strings.Contains(blob, "<<<<<<<") {
		t.Fatalf("pushed commit carries conflict markers:\n%s", blob)
	}
	if w := warnings.String(); !strings.Contains(w, "stale base") {
		t.Fatalf("expected a stale-base warning, got: %q", w)
	}
}

func TestPushChangeJJWithoutTrailerTemplateIsStructured(t *testing.T) {
	requireJJ(t)
	remote := newBareRemote(t)
	dir := newColocatedJJRepo(t) // deliberately NO SetupJJChangeIDs

	jjCommitFile(t, dir, "README.md", "hi\n", "initial")
	seed, _ := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if _, err := runGit(dir, "push", remote, seed+":refs/heads/main"); err != nil {
		t.Fatalf("seed trunk: %v", err)
	}
	jjCommitFile(t, dir, "proj/a.txt", "a\n", "change A")

	_, err := PushChange(dir, remote, "main")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "jj_change_ids_not_configured" {
		t.Fatalf("want jj_change_ids_not_configured (never amend behind jj's back), got %v", err)
	}
}

func TestDoctorReportsJJWiring(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)

	report, err := RunDoctor(dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if !report.IsJJWorkspace || report.JJChangeIDsWired {
		t.Fatalf("pre-setup: want jj detected + not wired, got %+v", report)
	}

	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}
	report, err = RunDoctor(dir, "main")
	if err != nil || !report.JJChangeIDsWired {
		t.Fatalf("post-setup: want wired, got %+v (%v)", report, err)
	}

	// Idempotent re-run must not error or clobber.
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("second SetupJJChangeIDs: %v", err)
	}
}

func TestSetupJJChangeIDsRefusesToClobberForeignTrailers(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	if _, err := runJJ(dir, "config", "set", "--repo", "templates.commit_trailers", `format_signed_off_by_trailer(self)`); err != nil {
		t.Fatalf("set foreign trailers: %v", err)
	}

	err := SetupJJChangeIDs(dir)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "jj_trailers_conflict" {
		t.Fatalf("want jj_trailers_conflict, got %v", err)
	}
}

// TestJJEvolveWorkflowEndToEnd is the workflow this direction exists for
// (§7.4, "changing something at the root is a critical workflow"): build a
// 3-Change stack in jj, push once, REWORK THE ROOT - jj auto-rebases the
// descendants (its evolve) - push once more, and every Change on the
// server has moved together with its identity intact. Client is the real
// `runko change push`; server is the real receive funnel.
func TestJJEvolveWorkflowEndToEnd(t *testing.T) {
	requireJJ(t)
	remote := newBareRemote(t)
	dir := newColocatedJJRepo(t)
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}

	jjCommitFile(t, dir, "proj/PROJECT.yaml", "schema: project/v1\nname: alpha\ntype: library\n", "initial")
	seed, _ := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if _, err := runGit(dir, "push", remote, seed+":refs/heads/main"); err != nil {
		t.Fatalf("seed trunk: %v", err)
	}

	jjCommitFile(t, dir, "proj/a.txt", "a v1\n", "change A")
	jjCommitFile(t, dir, "proj/b.txt", "b\n", "change B")
	jjCommitFile(t, dir, "proj/c.txt", "c\n", "change C")

	if _, err := PushChange(dir, remote, "main"); err != nil {
		t.Fatalf("initial stack push: %v", err)
	}
	tip1, _ := runGit(remote, "rev-parse", "refs/for/main")

	store := runkod.NewMemStore()
	p := &runkod.Processor{RepoDir: remote, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	ctx := context.Background()
	if res := p.Process(ctx, runkod.RefUpdate{OldSHA: zeroOIDForTest, NewSHA: tip1, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("funnel rejected the stack: %+v", res)
	}

	// Collect each Change's id from its trailer, and its server row.
	idOf := func(desc string) string {
		msg, err := runJJ(dir, "log", "--no-graph", "-r", `description(glob:"`+desc+`*")`, "-T", "description")
		if err != nil {
			t.Fatalf("read %s description: %v", desc, err)
		}
		id, ok := receive.ParseChangeID(msg)
		if !ok {
			t.Fatalf("%s has no trailer:\n%s", desc, msg)
		}
		return id
	}
	idA, idB, idC := idOf("change A"), idOf("change B"), idOf("change C")
	beforeA, _, _ := store.GetChange(ctx, idA)

	// THE evolve moment: rework the ROOT. jj rebases B and C by itself.
	if _, err := runJJ(dir, "edit", `description(glob:"change A*")`); err != nil {
		t.Fatalf("jj edit root: %v", err)
	}
	writeTestFile(t, dir, "proj/a.txt", "a v2 - reworked at the root\n")
	if _, err := runJJ(dir, "new", `description(glob:"change C*")`); err != nil {
		t.Fatalf("jj new back to tip: %v", err)
	}

	if _, err := PushChange(dir, remote, "main"); err != nil {
		t.Fatalf("post-evolve push: %v", err)
	}
	tip2, _ := runGit(remote, "rev-parse", "refs/for/main")
	if tip2 == tip1 {
		t.Fatal("evolve should have rewritten the tip")
	}
	if res := p.Process(ctx, runkod.RefUpdate{OldSHA: tip1, NewSHA: tip2, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("funnel rejected the evolved stack: %+v", res)
	}

	afterA, _, _ := store.GetChange(ctx, idA)
	afterB, _, _ := store.GetChange(ctx, idB)
	afterC, _, _ := store.GetChange(ctx, idC)
	if afterA.HeadSHA == beforeA.HeadSHA {
		t.Fatal("root Change's head did not move")
	}
	if afterB.BaseSHA != afterA.HeadSHA || afterC.BaseSHA != afterB.HeadSHA || afterC.HeadSHA != tip2 {
		t.Fatalf("stack not re-chained after evolve: A.head=%s B.base=%s B.head=%s C.base=%s C.head=%s tip=%s",
			afterA.HeadSHA, afterB.BaseSHA, afterB.HeadSHA, afterC.BaseSHA, afterC.HeadSHA, tip2)
	}
}
