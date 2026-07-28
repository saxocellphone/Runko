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

// jjTrailerRepo is a colocated jj repo already configured to derive
// Change-Id trailers - the state `runko doctor --install-hook` leaves, and
// the precondition every createChangeJJ path assumes.
func jjTrailerRepo(t *testing.T) string {
	t.Helper()
	dir := newColocatedJJRepo(t)
	if err := SetupJJChangeIDs(dir); err != nil {
		t.Fatalf("SetupJJChangeIDs: %v", err)
	}
	return dir
}

// The identity contract that motivates createChangeJJ: a Change created
// through jj must keep its id across an arbitrary later rewrite, because the
// trailer re-renders from the same jj change id every time.
//
// This exercises the `jj describe -m` form specifically, which DISCARDS the
// old message and so emits exactly one trailer. That is not jj's behavior in
// general: a rewrite that PRESERVES the description (`jj squash`, i.e. every
// `runko change amend`) appends a second trailer instead of replacing the
// first - see TestAmendChangeJJDedupsDuplicateChangeIDTrailers, which covers
// that path and the dedup it forces. An earlier version of this comment
// generalized "describe replaces" into "jj replaces", which is how the
// duplicate-trailer defect got written.
func TestCreateChangeJJIdentitySurvivesRewrite(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "proj/a.txt", "work\n")

	id, err := CreateChange(dir, "Reject invalid SKUs", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if !strings.HasPrefix(id, "I") {
		t.Fatalf("expected a Change-Id, got %q", id)
	}
	// `change create` parks a fresh empty @ above the described commit, so
	// the change itself is @- (the shape jjTipCommit expects).
	desc, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := receive.ParseChangeID(desc); !ok || got != id {
		t.Fatalf("described commit carries %q, want %q", got, id)
	}

	if _, err := runJJ(dir, "describe", "-r", "@-", "-m", "Reject invalid SKUs (reworded)"); err != nil {
		t.Fatalf("jj describe: %v", err)
	}
	after, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := receive.ParseChangeID(after)
	if !ok {
		t.Fatal("rewrite dropped the Change-Id trailer entirely")
	}
	if got != id {
		t.Fatalf("Change-Id drifted across a jj rewrite: created %s, after rewrite %s "+
			"(a drifting id mints a duplicate Change on the server for the same work)", id, got)
	}
}

// Work outside jj's sparse set is INVISIBLE to jj - `jj status` reports no
// changes - so a change created over it would silently omit the edit. The
// plain-git path refuses this via `outside_sparse_cone`; the jj path must
// refuse it too, in jj's own dialect.
//
// Critical: jj sparse de-materialization itself shows as ` D path` in git
// status for every file outside the new cone. A vacuous guard that only
// scrapes those deletions would pass this test without any real out-of-cone
// write — so the edit below must be ON DISK work the user actually made,
// and sibling cases assert the phantom-deletion / in-cone paths stay quiet.
func TestCreateChangeJJRefusesWorkOutsideSparseCone(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "keep/a.txt", "a\n")
	writeTestFile(t, dir, "drop/b.txt", "b\n")
	if _, err := CreateChange(dir, "seed both trees", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "keep"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	// Real lost work: a file written outside the cone, present on disk.
	// drop/ was de-materialized by sparse set (not on disk); recreate it.
	writeTestFile(t, dir, "drop/b.txt", "edit jj cannot see\n")
	if _, err := os.Stat(filepath.Join(dir, "drop/b.txt")); err != nil {
		t.Fatalf("out-of-cone write must be on disk (else this test is vacuous): %v", err)
	}

	_, err := CreateChange(dir, "should refuse", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "outside_sparse_cone" {
		t.Fatalf("want outside_sparse_cone, got %v", err)
	}
	if !strings.Contains(ce.Message, "drop/b.txt") {
		t.Fatalf("error should name the invisible file, got %q", ce.Message)
	}
	if !strings.Contains(ce.Suggestion, "jj sparse set") {
		t.Fatalf("suggestion should speak jj's dialect, got %q", ce.Suggestion)
	}
}

// Sparse de-materialization must not brick ordinary in-cone creates: after
// `jj sparse set --clear --add keep`, git status still lists every removed
// out-of-cone path as ` D ...`, but those are not on disk and are not lost
// work. create must succeed on a keep/ edit alone.
func TestCreateChangeJJAllowsInConeEditsUnderSparse(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "keep/a.txt", "a\n")
	writeTestFile(t, dir, "drop/b.txt", "b\n")
	if _, err := CreateChange(dir, "seed both trees", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "keep"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	// Confirm the phantom deletion is visible to git but not on disk — the
	// scenario that used to refuse every subsequent create.
	if st, err := runGit(dir, "status", "--porcelain", "-uall"); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(st, "drop/b.txt") {
		t.Fatalf("expected git status to list de-materialized drop/b.txt, got %q", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "drop/b.txt")); !os.IsNotExist(err) {
		t.Fatalf("drop/b.txt should not be on disk after sparse set, stat=%v", err)
	}

	writeTestFile(t, dir, "keep/a.txt", "in-cone edit\n")
	id, err := CreateChange(dir, "ordinary in-cone work", false)
	if err != nil {
		t.Fatalf("in-cone create under sparse must not refuse (phantom-deletion brick): %v", err)
	}
	if id == "" {
		t.Fatal("expected a Change-Id")
	}
}

// git status porcelain (without -z) quotes paths with spaces; a guard that
// slices line[3:] keeps the quotes and fails withinPrefixes against `keep`.
// Spaces in in-cone names must not be refused, and an out-of-cone spaced
// path must still be detected under its real unquoted name (os.Stat of a
// quoted path would miss it — so -z is load-bearing, not optional once
// the on-disk filter is in place).
func TestCreateChangeJJAllowsInConeFileWithSpace(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "keep/a.txt", "a\n")
	writeTestFile(t, dir, "drop/b.txt", "b\n")
	if _, err := CreateChange(dir, "seed both trees", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "keep"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	writeTestFile(t, dir, "keep/my file.txt", "space name\n")
	// Without -z this is `?? "keep/my file.txt"` — the quoting the -z path
	// exists to avoid.
	if st, err := runGit(dir, "status", "--porcelain", "-uall"); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(st, `"`) {
		t.Fatalf("expected git to quote the spaced path (else this case is vacuous): %q", st)
	}

	// Direct guard check: in-cone spaced path must not appear; an out-of-cone
	// spaced write must appear unquoted (bites a Stat-only fix that still
	// scrapes the quoted porcelain form).
	writeTestFile(t, dir, "drop/out there.txt", "hidden with space\n")
	lost, err := jjOutsideSparseChecked(dir)
	if err != nil {
		t.Fatalf("jjOutsideSparseChecked: %v", err)
	}
	for _, p := range lost {
		if strings.Contains(p, `"`) {
			t.Fatalf("jjOutsideSparse paths must be unquoted, got %q in %v", p, lost)
		}
		if p == "keep/my file.txt" || strings.HasPrefix(p, "keep/") {
			t.Fatalf("in-cone path must not be reported as lost: %v", lost)
		}
	}
	foundOut := false
	for _, p := range lost {
		if p == "drop/out there.txt" {
			foundOut = true
		}
	}
	if !foundOut {
		t.Fatalf("out-of-cone spaced path must be detected unquoted; got %v", lost)
	}

	// Only the in-cone file remains for the create (drop the out-of-cone bait).
	if err := os.Remove(filepath.Join(dir, "drop/out there.txt")); err != nil {
		t.Fatal(err)
	}

	id, err := CreateChange(dir, "in-cone file with space", false)
	if err != nil {
		t.Fatalf("in-cone path with space must not be refused: %v", err)
	}
	if id == "" {
		t.Fatal("expected a Change-Id")
	}
	summary, err := runJJ(dir, "diff", "-r", "@-", "--summary")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "my file.txt") {
		t.Fatalf("change should include the spaced file; diff was %q", summary)
	}
}

// jj sparse list emits one pattern per line. strings.Fields would shatter
// "my dir" into {"my","dir"}, so every path under that pattern fails
// withinPrefixes and the guard refuses IN-CONE work forever. Spaced
// FILENAMES (TestCreateChangeJJAllowsInConeFileWithSpace) are a different
// bug; this pins the PATTERN split.
func TestJJSparsePrefixesPreservesSpacesInPattern(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "my dir/a.txt", "a\n")
	writeTestFile(t, dir, "drop/b.txt", "b\n")
	if _, err := CreateChange(dir, "seed both trees", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "my dir"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	// Evidence the raw list keeps the space (vacuity check).
	raw, err := runJJ(dir, "sparse", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "my dir") {
		t.Fatalf("jj sparse list should emit the spaced pattern, got %q", raw)
	}
	if strings.Contains(raw, "\nmy\n") || strings.HasPrefix(raw, "my\n") {
		t.Fatalf("unexpected shattered raw list: %q", raw)
	}

	prefixes, err := jjSparsePrefixesChecked(dir)
	if err != nil {
		t.Fatalf("jjSparsePrefixesChecked: %v", err)
	}
	found := false
	for _, p := range prefixes {
		if p == "my" || p == "dir" {
			t.Fatalf("sparse pattern shattered on whitespace: got %#v (want intact \"my dir\")", prefixes)
		}
		if p == "my dir" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want pattern %q in prefixes, got %#v", "my dir", prefixes)
	}

	// In-cone edit under the spaced pattern must not look lost; out-of-cone
	// still must.
	writeTestFile(t, dir, "my dir/a.txt", "in-cone under spaced pattern\n")
	writeTestFile(t, dir, "drop/b.txt", "out of cone\n")
	lost, err := jjOutsideSparseChecked(dir)
	if err != nil {
		t.Fatalf("jjOutsideSparseChecked: %v", err)
	}
	for _, p := range lost {
		if p == "my dir/a.txt" || strings.HasPrefix(p, "my dir/") {
			t.Fatalf("in-cone path under spaced pattern must not be lost: %v", lost)
		}
	}
	foundOut := false
	for _, p := range lost {
		if p == "drop/b.txt" {
			foundOut = true
		}
	}
	if !foundOut {
		t.Fatalf("out-of-cone path must still be detected; got %v", lost)
	}

	if err := os.Remove(filepath.Join(dir, "drop/b.txt")); err != nil {
		t.Fatal(err)
	}
	id, err := CreateChange(dir, "in-cone under spaced sparse pattern", false)
	if err != nil {
		t.Fatalf("in-cone create under spaced sparse pattern must not refuse: %v", err)
	}
	if id == "" {
		t.Fatal("expected a Change-Id")
	}
}

// A dangling symlink outside the cone is a directory entry jj will not
// snapshot. os.Stat follows it, fails with IsNotExist, and the old guard
// treated it as a phantom deletion — silent work loss. Lstat sees the link.
func TestJJOutsideSparseCatchesBrokenSymlink(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "keep/a.txt", "a\n")
	writeTestFile(t, dir, "drop/b.txt", "b\n")
	if _, err := CreateChange(dir, "seed both trees", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "keep"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	// drop/ was de-materialized; recreate and plant a dangling link.
	if err := os.MkdirAll(filepath.Join(dir, "drop"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "drop/link")
	if err := os.Symlink("/nonexistent/runko-jj-guard-target", link); err != nil {
		t.Fatal(err)
	}
	// Vacuity: Stat misses it (the defect); Lstat sees it.
	if _, err := os.Stat(link); !os.IsNotExist(err) {
		t.Fatalf("expected Stat of dangling symlink to be IsNotExist (else this case is vacuous), got %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("Lstat must see the dangling symlink: %v", err)
	}

	lost, err := jjOutsideSparseChecked(dir)
	if err != nil {
		t.Fatalf("jjOutsideSparseChecked: %v", err)
	}
	found := false
	for _, p := range lost {
		if p == "drop/link" {
			found = true
		}
	}
	if !found {
		t.Fatalf("dangling out-of-cone symlink must be reported as lost work; got %v", lost)
	}
}

// A symlink loop also fails Stat (ELOOP, not IsNotExist) while Lstat
// succeeds on the link inode. Either way the path is real work outside the
// cone; the guard must name it.
func TestJJOutsideSparseCatchesSymlinkLoop(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "keep/a.txt", "a\n")
	writeTestFile(t, dir, "drop/b.txt", "b\n")
	if _, err := CreateChange(dir, "seed both trees", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "keep"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "drop"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(dir, "drop/loop_a")
	b := filepath.Join(dir, "drop/loop_b")
	if err := os.Symlink("loop_b", a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("loop_a", b); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a); err == nil {
		t.Fatal("expected Stat of symlink loop to fail (else this case is vacuous)")
	} else if os.IsNotExist(err) {
		t.Fatalf("symlink loop should not be IsNotExist (that would hide a different bug): %v", err)
	}
	if _, err := os.Lstat(a); err != nil {
		t.Fatalf("Lstat must see loop_a: %v", err)
	}

	lost, err := jjOutsideSparseChecked(dir)
	if err != nil {
		t.Fatalf("jjOutsideSparseChecked: %v", err)
	}
	foundA, foundB := false, false
	for _, p := range lost {
		if p == "drop/loop_a" {
			foundA = true
		}
		if p == "drop/loop_b" {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Fatalf("symlink-loop entries must be reported as lost work; got %v", lost)
	}
}

// A valid (resolving) out-of-cone symlink must still be caught — Lstat sees
// the link, and the previous Stat path also did; keep that regression-locked.
func TestJJOutsideSparseCatchesValidSymlink(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "keep/a.txt", "a\n")
	writeTestFile(t, dir, "drop/b.txt", "b\n")
	if _, err := CreateChange(dir, "seed both trees", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "keep"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "drop"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Point at an absolute existing path so the link resolves.
	target := filepath.Join(dir, "keep/a.txt")
	if err := os.Symlink(target, filepath.Join(dir, "drop/oklink")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "drop/oklink")); err != nil {
		t.Fatalf("valid symlink must resolve for this case: %v", err)
	}

	lost, err := jjOutsideSparseChecked(dir)
	if err != nil {
		t.Fatalf("jjOutsideSparseChecked: %v", err)
	}
	found := false
	for _, p := range lost {
		if p == "drop/oklink" {
			found = true
		}
	}
	if !found {
		t.Fatalf("valid out-of-cone symlink must be reported as lost work; got %v", lost)
	}
}

// Unrestricted working copies print a lone "." — that is success with no
// prefixes, not a command failure. The fail-closed rewrite must keep that
// convention or every unrestricted checkout would refuse create.
func TestJJSparsePrefixesUnrestrictedIsNilNotError(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	raw, err := runJJ(dir, "sparse", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(raw) != "." {
		t.Fatalf("fresh colocated repo should be unrestricted (\".\"), got %q", raw)
	}
	prefixes, err := jjSparsePrefixesChecked(dir)
	if err != nil {
		t.Fatalf("unrestricted sparse list must not error: %v", err)
	}
	if prefixes != nil {
		t.Fatalf("unrestricted must return nil prefixes, got %v", prefixes)
	}
	lost, err := jjOutsideSparseChecked(dir)
	if err != nil {
		t.Fatalf("jjOutsideSparseChecked unrestricted: %v", err)
	}
	if lost != nil {
		t.Fatalf("unrestricted cone cannot have out-of-cone paths, got %v", lost)
	}
}

// Work-loss guards must fail closed when the underlying command fails. A
// non-repo path makes `jj sparse list` / `jj status` exit non-zero; the
// Checked forms surface that instead of returning nil (= "nothing wrong").
func TestJJGuardsFailClosedOnCommandError(t *testing.T) {
	requireJJ(t)
	missing := filepath.Join(t.TempDir(), "not-a-jj-repo")

	_, err := jjSparsePrefixesChecked(missing)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "jj_sparse_list_failed" {
		t.Fatalf("want jj_sparse_list_failed, got %v", err)
	}

	_, err = jjOutsideSparseChecked(missing)
	if !errors.As(err, &ce) || ce.Code != "jj_sparse_list_failed" {
		t.Fatalf("jjOutsideSparseChecked should surface sparse-list failure, got %v", err)
	}

	_, err = jjSnapshotRefusalsChecked(missing)
	if !errors.As(err, &ce) || ce.Code != "jj_status_failed" {
		t.Fatalf("want jj_status_failed, got %v", err)
	}
}

// runJJStderr pins cmd.Dir to the repo so jj's CWD-relative path output is
// repo-relative. Without it, a process CWD far from the repo makes every
// path-bearing jj output unmatchable by repo-relative os.Lstat — the
// artifact/sparse guards skip every candidate and never fire. go test's CWD
// is the package dir, so this only bites when we chdir away first.
func TestRunJJStderrPathsAreRepoRelativeFromForeignCWD(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	writeTestFile(t, dir, "proj/tracked.txt", "v1\n")
	if _, err := runJJ(dir, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	writeTestFile(t, dir, "proj/tracked.txt", "v2\n")

	foreign := t.TempDir()
	// Foreign CWD must not be under dir, or relative paths could still resolve.
	if strings.HasPrefix(foreign, dir) || strings.HasPrefix(dir, foreign) {
		t.Fatalf("temp dirs unexpectedly nested: repo=%s foreign=%s", dir, foreign)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(foreign); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(wd)
	})

	out, _, err := runJJStderr(dir, "diff", "--summary")
	if err != nil {
		t.Fatalf("runJJStderr from foreign CWD: %v", err)
	}
	// Without cmd.Dir, jj prints CWD-relative climbs like
	// "M ../tmp.XXX/proj/tracked.txt". strings.Contains(out, "proj/tracked.txt")
	// still matches that, so demand the path TOKEN equal the repo-relative form.
	found := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "tracked.txt") {
			continue
		}
		fields := strings.Fields(line)
		path := fields[len(fields)-1]
		if path != "proj/tracked.txt" {
			t.Fatalf("jj path must be repo-relative when CWD is elsewhere (cmd.Dir missing?): got %q in line %q", path, line)
		}
		found = true
	}
	if !found {
		t.Fatalf("expected a diff line for proj/tracked.txt, got %q", out)
	}
}

// assertJJDiffPathRepoRelative runs `jj diff --summary` via runJJStderr and
// requires the path token for wantFile to equal the repo-relative form.
// Without jjRepoRoot, a subdirectory -R hard-fails ("There is no jj repo"),
// and a foreign CWD without cmd.Dir emits climbs like "../tmp/.../proj/f".
func assertJJDiffPathRepoRelative(t *testing.T, repoDir, wantFile string) {
	t.Helper()
	out, _, err := runJJStderr(repoDir, "diff", "--summary")
	if err != nil {
		t.Fatalf("runJJStderr(%q, diff --summary): %v", repoDir, err)
	}
	found := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, filepath.Base(wantFile)) {
			continue
		}
		fields := strings.Fields(line)
		path := fields[len(fields)-1]
		if path != wantFile {
			t.Fatalf("jj path must be repo-relative; got %q in line %q (repoDir=%q)", path, line, repoDir)
		}
		found = true
	}
	if !found {
		t.Fatalf("expected a diff line for %q, got %q (repoDir=%q)", wantFile, out, repoDir)
	}
}

// jjRepoRoot normalizes BOTH -R and cmd.Dir. Pinning only one of the two left
// a hole: -R <subdir> is a hard "There is no jj repo" (jj does not walk up),
// and a relative --dir evaluated from a foreign CWD silently pointed at the
// wrong place. These cases FAIL if normalization is removed.
func TestRunJJStderrNormalizesRepoRoot(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	writeTestFile(t, dir, "proj/tracked.txt", "v1\n")
	if _, err := runJJ(dir, "commit", "-m", "seed"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	writeTestFile(t, dir, "proj/tracked.txt", "v2\n")
	// A real subdirectory of the colocated checkout - the ordinary
	// `--dir .` / isJJWorkspace case when the process sits inside the tree.
	sub := filepath.Join(dir, "proj")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	// Absolute repo root must keep working (baseline).
	t.Run("absolute_root", func(t *testing.T) {
		assertJJDiffPathRepoRelative(t, dir, "proj/tracked.txt")
	})

	// Subdirectory as repoDir: without jjRepoRoot, -R is the subdir and jj
	// exits "There is no jj repo in <subdir>".
	t.Run("absolute_subdirectory", func(t *testing.T) {
		assertJJDiffPathRepoRelative(t, sub, "proj/tracked.txt")
	})

	foreign := t.TempDir()
	if strings.HasPrefix(foreign, dir) || strings.HasPrefix(dir, foreign) {
		t.Fatalf("temp dirs unexpectedly nested: repo=%s foreign=%s", dir, foreign)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(foreign); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	// Relative path to the repo root, evaluated from a foreign CWD.
	// filepath.Rel keeps it portable; without Abs-normalization a relative
	// -R can still work for the root itself, but paths and the subdir case
	// below still need the root pin.
	relRoot, err := filepath.Rel(foreign, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(relRoot) {
		t.Fatalf("expected a relative path from foreign CWD, got %q", relRoot)
	}
	t.Run("relative_root_from_foreign_cwd", func(t *testing.T) {
		assertJJDiffPathRepoRelative(t, relRoot, "proj/tracked.txt")
	})

	// Relative path into a SUBDIRECTORY of the repo from a foreign CWD —
	// the full bug: relative --dir that is not the top level.
	relSub, err := filepath.Rel(foreign, sub)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("relative_subdirectory_from_foreign_cwd", func(t *testing.T) {
		assertJJDiffPathRepoRelative(t, relSub, "proj/tracked.txt")
	})
}

// withinPrefixes is the cone-guard discriminator: a bare strings.HasPrefix
// would treat sparse pattern "alpha" as covering "alphabet/new.txt", so
// real out-of-cone work is reported as in-cone and silently dropped from
// the change. Boundary matching is the whole point of this helper.
func TestWithinPrefixesBoundary(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		prefixes []string
		want     bool
	}{
		{"string_prefix_not_dir", "alphabet/new.txt", []string{"alpha"}, false},
		{"dir_child", "alpha/new.txt", []string{"alpha"}, true},
		{"exact_file", "alpha", []string{"alpha"}, true},
		{"trailing_slash_child", "alpha/new.txt", []string{"alpha/"}, true},
		{"trailing_slash_exact", "alpha", []string{"alpha/"}, true},
		{"trailing_slash_string_prefix", "alphabet/new.txt", []string{"alpha/"}, false},
		{"src_vs_src_gen", "src-gen/x.go", []string{"src"}, false},
		{"src_vs_srcx", "srcx", []string{"src"}, false},
		{"src_child", "src/x.go", []string{"src"}, true},
		// Nested prefixes where one is a string prefix of another: each
		// path must match only under its own directory boundary.
		{"nested_shorter_wins", "a/b.txt", []string{"a", "ab"}, true},
		{"nested_longer_wins", "ab/c.txt", []string{"a", "ab"}, true},
		{"nested_neither_exact_string_prefix", "abc", []string{"a", "ab"}, false},
		{"nested_neither_child", "abc/d.txt", []string{"a", "ab"}, false},
		{"empty_prefixes", "anything", nil, false},
		{"multi_prefix_hit", "keep/a.txt", []string{"drop", "keep"}, true},
		{"multi_prefix_miss", "other/a.txt", []string{"drop", "keep"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := withinPrefixes(tc.path, tc.prefixes); got != tc.want {
				t.Fatalf("withinPrefixes(%q, %q) = %v, want %v", tc.path, tc.prefixes, got, tc.want)
			}
		})
	}
}

// Empirically (jj 0.43, hermetic empty HOME/XDG_CONFIG_HOME): describing
// with no user.email still exits zero; jj templates show an empty author,
// and the colocated git export writes author/committer as the literal
// sentinel JJ_EMPTY_STRING (not a hard failure). jj also warns those
// commits cannot be pushed to remotes. jjEnsureIdentity exists so the
// Runko fallback lands before any describe — matching the git path — not
// because describe itself refuses.
func TestJJEnsureIdentitySetsRunkoFallbackWhenUnset(t *testing.T) {
	requireJJ(t)
	// newColocatedJJRepo stamps a test identity; this path needs NONE.
	dir := t.TempDir()
	if out, err := exec.Command("jj", "git", "init", "--colocate", dir).CombinedOutput(); err != nil {
		t.Fatalf("jj git init --colocate: %v: %s", err, out)
	}

	// Baseline: config is empty, and a describe produces JJ_EMPTY_STRING in git.
	if email, err := runJJ(dir, "config", "get", "user.email"); err != nil {
		t.Fatalf("config get user.email: %v", err)
	} else if strings.TrimSpace(email) != "" {
		t.Fatalf("hermetic repo must have no user.email, got %q", email)
	}
	writeTestFile(t, dir, "probe.txt", "before ensure\n")
	if _, err := runJJ(dir, "describe", "-m", "no identity yet"); err != nil {
		t.Fatalf("describe without identity must succeed (observed jj 0.43): %v", err)
	}
	sha, err := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "commit_id")
	if err != nil {
		t.Fatal(err)
	}
	author, err := runGit(dir, "log", "-1", "--format=%an <%ae>", sha)
	if err != nil {
		t.Fatalf("git log author: %v", err)
	}
	if author != "JJ_EMPTY_STRING <JJ_EMPTY_STRING>" {
		t.Fatalf("expected git-exported empty identity sentinel, got %q (jj behavior changed?)", author)
	}

	if err := jjEnsureIdentity(dir); err != nil {
		t.Fatalf("jjEnsureIdentity: %v", err)
	}
	email, err := runJJ(dir, "config", "get", "user.email")
	if err != nil {
		t.Fatalf("config get after ensure: %v", err)
	}
	if strings.TrimSpace(email) != "runko@localhost" {
		t.Fatalf("want fallback email runko@localhost, got %q", email)
	}
	name, err := runJJ(dir, "config", "get", "user.name")
	if err != nil {
		t.Fatalf("config get user.name: %v", err)
	}
	if strings.TrimSpace(name) != "Runko" {
		t.Fatalf("want fallback name Runko, got %q", name)
	}

	// Fresh @ after ensure so the new identity applies to a new commit
	// (config changes do not rewrite prior empty-author commits).
	if _, err := runJJ(dir, "new"); err != nil {
		t.Fatalf("jj new: %v", err)
	}
	writeTestFile(t, dir, "after.txt", "with identity\n")
	if _, err := runJJ(dir, "describe", "-m", "with runko identity"); err != nil {
		t.Fatalf("describe after ensure: %v", err)
	}
	sha, err = runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "commit_id")
	if err != nil {
		t.Fatal(err)
	}
	author, err = runGit(dir, "log", "-1", "--format=%an <%ae>", sha)
	if err != nil {
		t.Fatalf("git log author after ensure: %v", err)
	}
	if author != "Runko <runko@localhost>" {
		t.Fatalf("post-ensure author want Runko <runko@localhost>, got %q", author)
	}
}

// A configured identity always wins — ensure must not clobber a real user.
func TestJJEnsureIdentityPreservesConfiguredIdentity(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t) // user.name=Test, user.email=test@runko.dev
	if err := jjEnsureIdentity(dir); err != nil {
		t.Fatalf("jjEnsureIdentity: %v", err)
	}
	email, err := runJJ(dir, "config", "get", "user.email")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(email) != "test@runko.dev" {
		t.Fatalf("ensure must not overwrite configured email; got %q", email)
	}
	name, err := runJJ(dir, "config", "get", "user.name")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(name) != "Test" {
		t.Fatalf("ensure must not overwrite configured name; got %q", name)
	}
}

// git status paths are repo-root-relative. Calling the cone guard with a
// subdirectory must still Lstat against the root — joining the subdir
// makes every real out-of-cone path IsNotExist and the guard goes quiet.
func TestJJOutsideSparseFromSubdirectory(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "keep/a.txt", "a\n")
	writeTestFile(t, dir, "drop/b.txt", "b\n")
	if _, err := CreateChange(dir, "seed both trees", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "keep"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	writeTestFile(t, dir, "drop/b.txt", "out of cone from subdir call\n")
	sub := filepath.Join(dir, "keep")

	lost, err := jjOutsideSparseChecked(sub)
	if err != nil {
		t.Fatalf("jjOutsideSparseChecked from subdirectory: %v", err)
	}
	found := false
	for _, p := range lost {
		if p == "drop/b.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("out-of-cone work must be found when repoDir is a subdirectory; got %v", lost)
	}
}

// jjOutsideSparseChecked fail-closes when git status cannot run. Corrupt
// the colocated index after sparse is set: jj sparse list still works
// (cone non-empty), but git status exits non-zero — the git_status_failed
// branch that had no coverage.
func TestJJOutsideSparseFailClosedOnGitStatusError(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "keep/a.txt", "a\n")
	writeTestFile(t, dir, "drop/b.txt", "b\n")
	if _, err := CreateChange(dir, "seed both trees", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := runJJ(dir, "sparse", "set", "--clear", "--add", "keep"); err != nil {
		t.Fatalf("jj sparse set: %v", err)
	}
	// Confirm the happy path reaches git status (prefixes non-empty).
	prefixes, err := jjSparsePrefixesChecked(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) == 0 {
		t.Fatal("expected non-empty sparse prefixes so git status is consulted")
	}
	// Honest failure: a truncated index makes `git status` exit 128 while
	// leaving the jj repo readable enough for sparse list.
	if err := os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(dir, "status", "--porcelain", "-uall", "-z"); err == nil {
		t.Fatal("expected git status to fail on corrupt index (else this case is vacuous)")
	}

	_, err = jjOutsideSparseChecked(dir)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "git_status_failed" {
		t.Fatalf("want git_status_failed, got %v", err)
	}
	if !strings.Contains(ce.Suggestion, "git status") {
		t.Fatalf("suggestion should name git status, got %q", ce.Suggestion)
	}
}

// jjTrailerConfiguredChecked must not collapse "jj could not run" into
// "template not set" — that misdiagnosis points at doctor --install-hook
// while the trailers are fine (or jj is simply broken).
func TestJJTrailerConfiguredCheckedDistinguishesUnsetFromFailure(t *testing.T) {
	requireJJ(t)

	t.Run("unset", func(t *testing.T) {
		dir := newColocatedJJRepo(t) // no SetupJJChangeIDs
		ok, err := jjTrailerConfiguredChecked(dir)
		if err != nil {
			t.Fatalf("unset template must be (false, nil), got err %v", err)
		}
		if ok {
			t.Fatal("expected not configured")
		}
	})

	t.Run("configured", func(t *testing.T) {
		dir := jjTrailerRepo(t)
		ok, err := jjTrailerConfiguredChecked(dir)
		if err != nil || !ok {
			t.Fatalf("want (true, nil), got (%v, %v)", ok, err)
		}
	})

	t.Run("jj_failure", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "not-a-jj-repo")
		ok, err := jjTrailerConfiguredChecked(missing)
		if err == nil {
			t.Fatalf("want an error when jj cannot run, got ok=%v", ok)
		}
		if ok {
			t.Fatal("failure must not report configured")
		}
		// The bool wrapper fail-opens: used by doctor display only.
		if jjTrailerConfigured(missing) {
			t.Fatal("jjTrailerConfigured must be false when jj cannot run")
		}
	})
}

// jj EXCLUDES a file over snapshot.max-new-file-size from the working copy
// and merely warns on stderr while exiting zero, so an unguarded create
// would ship a change missing exactly that file. Refuse loudly instead, and
// make --allow-large lift jj's cap so the opt-in actually lands the file.
func TestCreateChangeJJRefusesFilesJJWontSnapshot(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	// Comfortably over jj's 1MiB default, under runko's 5MiB heuristic, so
	// this exercises jj's refusal rather than the artifact size rule.
	writeTestFile(t, dir, "assets/big.bin", strings.Repeat("x", 2<<20))

	_, err := CreateChange(dir, "should refuse", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "suspect_artifact" {
		t.Fatalf("want suspect_artifact, got %v", err)
	}
	if !strings.Contains(ce.Message, "assets/big.bin") {
		t.Fatalf("error should name the unsnapshotted file, got %q", ce.Message)
	}

	// --allow-large must not merely bypass the guard: the file has to end up
	// IN the change, which needs jj's own cap lifted for the describe.
	id, err := CreateChange(dir, "intentional asset", true)
	if err != nil {
		t.Fatalf("CreateChange --allow-large: %v", err)
	}
	if id == "" {
		t.Fatal("expected a Change-Id")
	}
	summary, err := runJJ(dir, "--config", "snapshot.max-new-file-size=1073741824",
		"diff", "-r", "@-", "--summary")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, "assets/big.bin") {
		t.Fatalf("--allow-large did not land the file in the change; diff was %q", summary)
	}
}

// A clean working copy has nothing to name: jj auto-snapshots continuously,
// so "empty @" is the jj spelling of "no staged changes".
func TestCreateChangeJJRefusesEmptyWorkingCopy(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "proj/a.txt", "work\n")
	if _, err := CreateChange(dir, "first", false); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := CreateChange(dir, "nothing new", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "nothing_to_commit" {
		t.Fatalf("want nothing_to_commit, got %v", err)
	}
}

// Without the trailer template there is no stable identity to derive, so
// the jj path must refuse up front rather than mint a git-style id jj would
// later overwrite.
func TestCreateChangeJJRequiresTrailerTemplate(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t) // deliberately NOT SetupJJChangeIDs
	writeTestFile(t, dir, "proj/a.txt", "work\n")

	_, err := CreateChange(dir, "no identity available", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "jj_change_ids_not_configured" {
		t.Fatalf("want jj_change_ids_not_configured, got %v", err)
	}
}

// `change amend` in a jj checkout folds @ into the change below it via `jj
// squash`, which preserves the parent's jj change id - so the Change keeps
// its identity, with no trailer carried forward by hand. This verb used to
// refuse jj checkouts outright.
func TestAmendChangeJJFoldsAndKeepsIdentity(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "proj/a.txt", "v1\n")
	id, err := CreateChange(dir, "first cut", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}

	writeTestFile(t, dir, "proj/a.txt", "v2\n")
	writeTestFile(t, dir, "proj/b.txt", "new file\n")
	amended, err := AmendChange(dir, "", false)
	if err != nil {
		t.Fatalf("AmendChange: %v", err)
	}
	if amended != id {
		t.Fatalf("amend changed the Change-Id: %s -> %s", id, amended)
	}
	summary, err := runJJ(dir, "diff", "-r", "@-", "--summary")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"proj/a.txt", "proj/b.txt"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("amend did not fold %s into the change; diff was %q", want, summary)
		}
	}
	// The working copy is empty again: the stack shape `change push` expects.
	if empty, _ := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "empty"); strings.TrimSpace(empty) != "true" {
		t.Fatal("working copy should be empty after the squash")
	}
}

// A message-only amend rewords in place and still keeps the identity - the
// trailer template re-derives the same id from the unchanged jj change id.
func TestAmendChangeJJRewordKeepsIdentity(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "proj/a.txt", "v1\n")
	id, err := CreateChange(dir, "original wording", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	amended, err := AmendChange(dir, "clearer wording", false)
	if err != nil {
		t.Fatalf("AmendChange reword: %v", err)
	}
	if amended != id {
		t.Fatalf("reword changed the Change-Id: %s -> %s", id, amended)
	}
	desc, _ := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "description")
	if !strings.Contains(desc, "clearer wording") {
		t.Fatalf("reword did not take; description was %q", desc)
	}
}
