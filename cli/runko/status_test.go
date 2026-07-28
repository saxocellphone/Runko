package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/checks"
)

var (
	statusTestIDReady   = fakeChangeID("status-ready")
	statusTestIDBlocked = fakeChangeID("status-blocked")
)

// statusFixture builds the shape status reads: a trunk commit marked as
// the remote-tracking ref, a two-change stack above it (both with
// Change-Id trailers), and one uncommitted file.
func statusFixture(t *testing.T) *gitfixture.Repo {
	t.Helper()
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("base")
	repo.Run("update-ref refs/remotes/origin/main HEAD")
	repo.WriteFile("a.txt", "a\n")
	repo.Commit("bottom change\n\nChange-Id: " + statusTestIDReady)
	repo.WriteFile("b.txt", "b\n")
	repo.Commit("top change\n\nChange-Id: " + statusTestIDBlocked)
	repo.WriteFile("wip.txt", "uncommitted\n")
	return repo
}

func TestRunStatusLocalOnly(t *testing.T) {
	repo := statusFixture(t)
	repo.Run("config runko.workspace ws1")

	r, err := RunStatus(context.Background(), http.DefaultClient, nil, "no stored credential", repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if r.WorkspaceID != "ws1" || r.Branch != "head" {
		t.Fatalf("expected workspace ws1 @ head, got %q @ %q", r.WorkspaceID, r.Branch)
	}
	if r.DirtyPaths != 1 {
		t.Fatalf("expected 1 dirty path (wip.txt), got %d", r.DirtyPaths)
	}
	if r.ServerError != "no stored credential" {
		t.Fatalf("expected the credential error relayed, got %q", r.ServerError)
	}
	if r.StaleBase {
		t.Fatalf("an unreachable/unconfigured remote must read as not-stale, got StaleBase=true")
	}
	if r.TrunkSHA == "" || r.TrunkTitle != "base" {
		t.Fatalf("expected the trunk base node facts (the graph's ◆), got %q / %q", r.TrunkSHA, r.TrunkTitle)
	}
	if len(r.Stack) != 2 {
		t.Fatalf("expected a 2-change stack, got %+v", r.Stack)
	}
	if r.Stack[0].ChangeID != statusTestIDReady || r.Stack[0].Title != "bottom change" {
		t.Fatalf("stack must be bottom -> top, got %+v", r.Stack)
	}
	if r.Stack[0].Status != "unknown" || r.Stack[1].Status != "unknown" {
		t.Fatalf("without a credential every entry is unknown, got %+v", r.Stack)
	}
}

func TestRunStatusServerEnrichment(t *testing.T) {
	repo := statusFixture(t)
	repo.Run("config runko.workspace ws1")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/api/whoami":
			json.NewEncoder(w).Encode(map[string]any{"name": "alice", "anonymous": false})
		case req.URL.Path == "/api/workspaces/ws1":
			json.NewEncoder(w).Encode(WorkspaceInfo{ID: "ws1", Status: "open"})
		case req.URL.Path == "/api/changes/"+statusTestIDReady,
			req.URL.Path == "/api/changes/"+statusTestIDBlocked:
			json.NewEncoder(w).Encode(ChangeInfo{State: "open"})
		case strings.Contains(req.URL.Path, statusTestIDReady):
			json.NewEncoder(w).Encode(checks.MergeRequirements{Mergeable: true})
		case strings.Contains(req.URL.Path, statusTestIDBlocked):
			json.NewEncoder(w).Encode(checks.MergeRequirements{
				Mergeable: false,
				Blockers:  []string{"required owner approval outstanding: admin"},
			})
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()

	cred := Credential{URL: srv.URL, Secret: "tok"}
	r, err := RunStatus(context.Background(), srv.Client(), &cred, "", repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if r.Principal != "alice" || r.ControlPlane != srv.URL {
		t.Fatalf("expected alice @ %s, got %q @ %q", srv.URL, r.Principal, r.ControlPlane)
	}
	if r.WorkspaceStatus != "open" {
		t.Fatalf("expected the server workspace status, got %q", r.WorkspaceStatus)
	}
	if r.Stack[0].Status != "ready" {
		t.Fatalf("expected the bottom change ready, got %+v", r.Stack[0])
	}
	if r.Stack[1].Status != "blocked" || len(r.Stack[1].Blockers) != 1 {
		t.Fatalf("expected the top change blocked with its blocker, got %+v", r.Stack[1])
	}
}

// TestRunStatusLandedChangeReportsTheServerState: a stale local trunk ref
// leaves already-landed commits in the base..tip range - they must read
// as landed (the server's own state), never as a "ready" stack (the wart
// the first live smoke test of this command surfaced).
func TestRunStatusLandedChangeReportsTheServerState(t *testing.T) {
	repo := statusFixture(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.URL.Path == "/api/whoami":
			json.NewEncoder(w).Encode(map[string]any{"name": "alice"})
		case strings.HasPrefix(req.URL.Path, "/api/changes/"):
			json.NewEncoder(w).Encode(ChangeInfo{State: "landed"})
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()

	cred := Credential{URL: srv.URL, Secret: "tok"}
	r, err := RunStatus(context.Background(), srv.Client(), &cred, "", repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	for i, e := range r.Stack {
		if e.Status != "landed" {
			t.Fatalf("stack[%d]: expected landed, got %+v", i, e)
		}
	}
}

func TestRunStatusUnpushedChangeReads404AsNotPushed(t *testing.T) {
	repo := statusFixture(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/api/whoami" {
			json.NewEncoder(w).Encode(map[string]any{"name": "alice"})
			return
		}
		http.NotFound(w, req)
	}))
	defer srv.Close()

	cred := Credential{URL: srv.URL, Secret: "tok"}
	r, err := RunStatus(context.Background(), srv.Client(), &cred, "", repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	for i, e := range r.Stack {
		if e.Status != "not_pushed" {
			t.Fatalf("stack[%d]: expected not_pushed for a change the control plane has never seen, got %+v", i, e)
		}
	}
}

// A frozen outbound mirror is deployment-wide and blocks NOTHING the
// person at this terminal is doing - landing keeps succeeding while no
// landed commit reaches the mirror, so post-land CI and every deploy
// silently stall. Prod sat in that state for 19h on 2026-07-24 because no
// surface said so; status is the surface that says so.
func TestRunStatusReportsAFrozenMirror(t *testing.T) {
	repo := statusFixture(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/whoami":
			json.NewEncoder(w).Encode(map[string]any{"name": "alice"})
		case "/api/mirror/status":
			json.NewEncoder(w).Encode(map[string]any{
				"Configured": true,
				"Cursors": []map[string]any{
					{"Ref": "refs/heads/main", "Frozen": true},
					{"Ref": "refs/tags/*", "Frozen": false},
				},
			})
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()

	cred := Credential{URL: srv.URL, Secret: "tok"}
	r, err := RunStatus(context.Background(), srv.Client(), &cred, "", repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if len(r.MirrorFrozenRefs) != 1 || r.MirrorFrozenRefs[0] != "refs/heads/main" {
		t.Fatalf("want only the frozen ref reported, got %+v", r.MirrorFrozenRefs)
	}

	var b strings.Builder
	PrintStatus(&b, r)
	out := b.String()
	if !strings.Contains(out, "MIRROR:") || !strings.Contains(out, "refs/heads/main") {
		t.Fatalf("a frozen mirror must be visible in the rendered status, got:\n%s", out)
	}
	if !strings.Contains(out, "deploys are stalled") {
		t.Fatalf("the line must say what it COSTS, not just that a ref is frozen, got:\n%s", out)
	}
}

// The healthy case stays silent, and a daemon that does not serve the
// route (or serves it without a mirror) must not turn status into a
// warning generator.
func TestRunStatusHealthyOrAbsentMirrorSaysNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		body map[string]any
		code int
	}{
		{name: "healthy", body: map[string]any{"Configured": true, "Cursors": []map[string]any{{"Ref": "refs/heads/main", "Frozen": false}}}},
		{name: "unconfigured", body: map[string]any{"Configured": false}},
		{name: "route absent", code: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := statusFixture(t)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch {
				case req.URL.Path == "/api/whoami":
					json.NewEncoder(w).Encode(map[string]any{"name": "alice"})
				case req.URL.Path == "/api/mirror/status" && tc.code == 0:
					json.NewEncoder(w).Encode(tc.body)
				default:
					http.NotFound(w, req)
				}
			}))
			defer srv.Close()

			cred := Credential{URL: srv.URL, Secret: "tok"}
			r, err := RunStatus(context.Background(), srv.Client(), &cred, "", repo.Dir, "origin", "main")
			if err != nil {
				t.Fatalf("RunStatus: %v", err)
			}
			if len(r.MirrorFrozenRefs) != 0 {
				t.Fatalf("nothing to warn about, got %+v", r.MirrorFrozenRefs)
			}
			var b strings.Builder
			PrintStatus(&b, r)
			if strings.Contains(b.String(), "MIRROR:") {
				t.Fatalf("healthy mirror must print no MIRROR line, got:\n%s", b.String())
			}
		})
	}
}

func TestRunStatusUnreachableServerKeepsLocalFacts(t *testing.T) {
	repo := statusFixture(t)

	// A closed port: whoami fails, but the local half must still answer.
	cred := Credential{URL: "http://127.0.0.1:1", Secret: "tok"}
	r, err := RunStatus(context.Background(), http.DefaultClient, &cred, "", repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus must not fail on an unreachable control plane: %v", err)
	}
	if r.ServerError == "" {
		t.Fatalf("expected ServerError to name the unreachable control plane")
	}
	if len(r.Stack) != 2 || r.DirtyPaths != 1 {
		t.Fatalf("local facts must survive an unreachable server, got %+v", r)
	}
}

func TestRunStatusNoTrunkRefReportsNilStack(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("only commit, no remote-tracking trunk")

	r, err := RunStatus(context.Background(), http.DefaultClient, nil, "no stored credential", repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if r.Stack != nil {
		t.Fatalf("with no local trunk ref the stack is unknowable, not the whole history: got %+v", r.Stack)
	}
}

// TestRunStatusTrailerlessCommitIsNotAChange: a commit with no Change-Id
// (jj's undescribed working-copy commit, a raw git commit in an unhooked
// checkout) is not a Change at all - it must say so, not "unknown", which
// reads like a lookup failure (dogfood feedback, 2026-07-23).
func TestRunStatusTrailerlessCommitIsNotAChange(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("base")
	repo.Run("update-ref refs/remotes/origin/main HEAD")
	repo.WriteFile("scratch.txt", "wip\n")
	repo.Commit("scratch commit, no trailer")

	r, err := RunStatus(context.Background(), http.DefaultClient, nil, "no stored credential", repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if len(r.Stack) != 1 || r.Stack[0].Status != "not_a_change" || r.Stack[0].ChangeID != "" {
		t.Fatalf("expected one not_a_change entry, got %+v", r.Stack)
	}
}

func TestRunStatusNotARepo(t *testing.T) {
	_, err := RunStatus(context.Background(), http.DefaultClient, nil, "", t.TempDir(), "origin", "main")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_a_repo" {
		t.Fatalf("expected structured not_a_repo, got %v", err)
	}
}

func statusPrintFixture() StatusReport {
	return StatusReport{
		Dir: "/w", WorkspaceID: "ws1", Branch: "head", WorkspaceStatus: "open",
		Remote: "origin", TrunkRef: "main", Principal: "alice", ControlPlane: "http://cp",
		TrunkSHA: "aaaabbbbccccdddd", TrunkTitle: "trunk tip subject",
		Stack: []StackEntry{
			{ChangeID: statusTestIDReady, Title: "bottom", Status: "ready"},
			{ChangeID: statusTestIDBlocked, Title: "top", Status: "blocked",
				Blockers: []string{"required owner approval outstanding: admin"}},
		},
	}
}

// TestPrintStatusDrawsTheJJStyleGraph: the line above trunk renders the
// way jj log draws it - newest on top, @ on the tip (the working copy's
// seat in a clean tree), ○ below, ◆ the trunk base, blockers on the
// node's │ gutter.
func TestPrintStatusDrawsTheJJStyleGraph(t *testing.T) {
	var b strings.Builder
	PrintStatus(&b, statusPrintFixture())
	out := b.String()
	for _, want := range []string{
		"workspace:    ws1 @ head (open)",
		"signed in:    alice @ http://cp",
		"@  " + statusTestIDBlocked + "  top  (✕ blocked)",
		"│      -> required owner approval outstanding: admin",
		"○  " + statusTestIDReady + "  bottom  (✓ ready)",
		"◆  aaaabbbbcccc origin/main  trunk tip subject",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in:\n%s", want, out)
		}
	}
	if strings.Index(out, "@  "+statusTestIDBlocked) > strings.Index(out, "○  "+statusTestIDReady) {
		t.Fatalf("the graph must render newest first (top of stack above bottom):\n%s", out)
	}
}

// TestPrintStatusDirtyWorkingTreeTakesTheAtSeat: with uncommitted paths
// the working tree itself is where @ sits (jj's model of the working
// copy), and every change drops to ○.
func TestPrintStatusDirtyWorkingTreeTakesTheAtSeat(t *testing.T) {
	r := statusPrintFixture()
	r.DirtyPaths = 3
	var b strings.Builder
	PrintStatus(&b, r)
	out := b.String()
	for _, want := range []string{
		"@  3 uncommitted path(s)",
		"○  " + statusTestIDBlocked + "  top  (✕ blocked)",
		"○  " + statusTestIDReady + "  bottom  (✓ ready)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in:\n%s", want, out)
		}
	}
}

// TestPrintStatusEmptyStackSaysNothingInFlight: an empty stack (clean or
// dirty) does not open a bare graph - dirt rides the working-tree line;
// the stack line says nothing is in flight.
func TestPrintStatusEmptyStackSaysNothingInFlight(t *testing.T) {
	for _, dirty := range []int{0, 1} {
		r := statusPrintFixture()
		r.Stack = []StackEntry{}
		r.DirtyPaths = dirty
		var b strings.Builder
		PrintStatus(&b, r)
		out := b.String()
		if !strings.Contains(out, "stack:        nothing in flight - HEAD is on trunk") {
			t.Fatalf("dirty=%d: expected the nothing-in-flight line:\n%s", dirty, out)
		}
		if strings.Contains(out, "◆  ") || strings.Contains(out, "stack (bottom") {
			t.Fatalf("dirty=%d: empty stack must not open a graph:\n%s", dirty, out)
		}
	}
}

// TestRunStatusPlainGitOnTrunkNothingInFlight: a plain-git checkout whose
// HEAD is the remote trunk tip has an empty stack and the nothing-in-flight
// human line (no phantom "0 change(s)" header).
func TestRunStatusPlainGitOnTrunkNothingInFlight(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("base")
	repo.Run("update-ref refs/remotes/origin/main HEAD")

	r, err := RunStatus(context.Background(), http.DefaultClient, nil, "no stored credential", repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if r.IsJJWorkspace {
		t.Fatalf("expected a plain-git checkout, got IsJJWorkspace=true")
	}
	if r.Stack == nil || len(r.Stack) != 0 {
		t.Fatalf("expected empty (non-nil) stack on trunk, got %+v", r.Stack)
	}
	var b strings.Builder
	PrintStatus(&b, r)
	if !strings.Contains(b.String(), "stack:        nothing in flight - HEAD is on trunk") {
		t.Fatalf("expected the nothing-in-flight line:\n%s", b.String())
	}
}

// TestRunStatusJJUndescribedWorkingCopyDropped: jj-colocated on trunk with
// a dirty undescribed @ must not list that WC commit as a stack entry -
// its content is already on the working-tree line (dogfood, 2026-07-23).
func TestRunStatusJJUndescribedWorkingCopyDropped(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	jjCommitFile(t, dir, "README.md", "hi\n", "base")
	base, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	if _, err := runGit(dir, "update-ref", "refs/remotes/origin/main", base); err != nil {
		t.Fatalf("seed origin/main: %v", err)
	}
	// Dirty file: jj folds it into the undescribed working-copy commit @.
	writeTestFile(t, dir, "wip.txt", "uncommitted\n")

	r, err := RunStatus(context.Background(), http.DefaultClient, nil, "no stored credential", dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if !r.IsJJWorkspace {
		t.Fatalf("expected IsJJWorkspace")
	}
	if r.DirtyPaths < 1 {
		t.Fatalf("expected dirty paths from wip.txt, got %d", r.DirtyPaths)
	}
	if len(r.Stack) != 0 {
		t.Fatalf("undescribed jj @ must not be a stack entry, got %+v", r.Stack)
	}
	var b strings.Builder
	PrintStatus(&b, r)
	out := b.String()
	if !strings.Contains(out, "stack:        nothing in flight - HEAD is on trunk") {
		t.Fatalf("expected the nothing-in-flight line:\n%s", out)
	}
	if strings.Contains(out, "not a change yet") || strings.Contains(out, "(no description set)") {
		t.Fatalf("must not double-report the undescribed WC as a stack node:\n%s", out)
	}
}

// TestRunStatusJJDescribedWorkingCopyStays: a working-copy commit the user
// wrote a message for is genuine WIP and remains on the stack.
func TestRunStatusJJDescribedWorkingCopyStays(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	jjCommitFile(t, dir, "README.md", "hi\n", "base")
	base, err := runJJ(dir, "log", "--no-graph", "-r", "@-", "-T", "commit_id")
	if err != nil {
		t.Fatalf("resolve base: %v", err)
	}
	if _, err := runGit(dir, "update-ref", "refs/remotes/origin/main", base); err != nil {
		t.Fatalf("seed origin/main: %v", err)
	}
	// Describe @ without committing: leaves a described working-copy commit.
	if _, err := runJJ(dir, "describe", "-m", "wip: real work in progress"); err != nil {
		t.Fatalf("jj describe: %v", err)
	}
	// Optional content so the WC is non-empty (matches a typical WIP seat).
	writeTestFile(t, dir, "wip.txt", "in progress\n")

	r, err := RunStatus(context.Background(), http.DefaultClient, nil, "no stored credential", dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if len(r.Stack) != 1 {
		t.Fatalf("described jj @ must stay on the stack, got %+v", r.Stack)
	}
	if r.Stack[0].Title != "wip: real work in progress" {
		t.Fatalf("expected the describe message as title, got %+v", r.Stack[0])
	}
	if r.Stack[0].ChangeID != "" || r.Stack[0].Status != "not_a_change" {
		t.Fatalf("described but trailer-less @ is not_a_change, got %+v", r.Stack[0])
	}
}

// TestPrintStatusTrailerlessCommitRendersSHAAndHint: with no Change-Id
// there is no identity to print - the node shows the commit's short SHA,
// jj's own wording for an empty subject, and the actionable hint instead
// of "? unknown".
func TestPrintStatusTrailerlessCommitRendersSHAAndHint(t *testing.T) {
	r := statusPrintFixture()
	r.Stack = []StackEntry{{SHA: "abcdef012345678901234567890123456789abcd", Status: "not_a_change"}}
	var b strings.Builder
	PrintStatus(&b, r)
	out := b.String()
	for _, want := range []string{
		"@  abcdef012345  (no description set)  (not a change yet - `runko change push` stamps its Change-Id)",
		"◆  aaaabbbbcccc origin/main",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unknown") {
		t.Fatalf("a trailer-less commit must not read as unknown:\n%s", out)
	}
}

func TestStatusCmdStrayPositionalIsUsageError(t *testing.T) {
	err := execCLI("status", "extra")
	var ue usageError
	if !errors.As(err, &ue) {
		t.Fatalf("expected a usage error for a stray positional, got %v", err)
	}
}

// --- jj dirtyPaths coverage ---
//
// Until these existed, `dirtyPaths(dir, true)` had zero callers in tests:
// flipping `if jj` to `if false` left the whole package green. They run
// real jj (requireJJ) and assert the count is what `change create` would
// actually sweep - including the sparse case where git status over-counts.

// jjSummaryCount is the ground truth for what createChangeJJ would include:
// one non-empty line per path in `jj diff -r @ --summary`.
func jjSummaryCount(t *testing.T, dir string) int {
	t.Helper()
	out, err := runJJ(dir, "diff", "-r", "@", "--summary")
	if err != nil {
		t.Fatalf("jj diff -r @ --summary: %v", err)
	}
	return countLines(out)
}

func TestDirtyPathsJJCleanWorkingCopy(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	jjCommitFile(t, dir, "README.md", "hi\n", "seed")

	// Empty undescribed @ after a commit: nothing for change create to take.
	if empty, err := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "empty"); err != nil {
		t.Fatal(err)
	} else if strings.TrimSpace(empty) != "true" {
		t.Fatalf("precondition: want empty @ after seed commit, got %q", empty)
	}
	if got, want := dirtyPaths(dir, true), 0; got != want {
		t.Fatalf("clean empty @: dirtyPaths=%d, want %d", got, want)
	}
	if got, want := dirtyPaths(dir, true), jjSummaryCount(t, dir); got != want {
		t.Fatalf("dirtyPaths (%d) must match jj summary (%d)", got, want)
	}
}

func TestDirtyPathsJJModifiedAndUntracked(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	jjCommitFile(t, dir, "a.txt", "a\n", "seed")
	// Two modifications + one new file (jj auto-tracks; the new path is in @).
	writeTestFile(t, dir, "a.txt", "a2\n")
	writeTestFile(t, dir, "b.txt", "b\n")
	writeTestFile(t, dir, "c.txt", "c\n")

	want := jjSummaryCount(t, dir)
	if want < 3 {
		t.Fatalf("precondition: want at least 3 paths in @, jj summary=%d", want)
	}
	if got := dirtyPaths(dir, true); got != want {
		t.Fatalf("dirtyPaths=%d, want jj summary count %d", got, want)
	}
	// createChangeJJ would take exactly those paths - refuse nothing_to_commit
	// only when the count is zero (covered separately after create).
}

func TestDirtyPathsJJEmptyAfterCreate(t *testing.T) {
	requireJJ(t)
	dir := jjTrailerRepo(t)
	writeTestFile(t, dir, "proj/a.txt", "work\n")
	if _, err := CreateChange(dir, "one change", false); err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	// create parks a fresh empty undescribed @ above the described commit.
	if empty, err := runJJ(dir, "log", "--no-graph", "-r", "@", "-T", "empty"); err != nil {
		t.Fatal(err)
	} else if strings.TrimSpace(empty) != "true" {
		t.Fatalf("post-create @ must be empty, got %q", empty)
	}
	if got := dirtyPaths(dir, true); got != 0 {
		t.Fatalf("empty @ after change create: dirtyPaths=%d, want 0 (nothing create would sweep)", got)
	}
	_, err := CreateChange(dir, "should refuse", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "nothing_to_commit" {
		t.Fatalf("create on empty @ must be nothing_to_commit (count matches create), got %v", err)
	}
}

// Sparse is the load-bearing difference between the jj and git branches:
// after `jj sparse set --clear --add keep`, git status still lists every
// de-materialized out-of-cone path as ` D ...`, while jj cannot see them
// and change create would not include them. A mutation that forces the git
// branch (`if jj` -> `if false`) reports that phantom and fails this test.
func TestDirtyPathsJJIgnoresOutOfConeGitNoise(t *testing.T) {
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
	writeTestFile(t, dir, "keep/a.txt", "in-cone edit\n")

	gitN := countLines(mustGitStatus(t, dir))
	jjN := jjSummaryCount(t, dir)
	if jjN != 1 {
		t.Fatalf("precondition: want 1 in-cone dirty path via jj, got %d", jjN)
	}
	if gitN <= jjN {
		t.Fatalf("precondition: git must over-count under sparse (git=%d, jj=%d); else this cannot kill the git-branch mutation", gitN, jjN)
	}
	if got := dirtyPaths(dir, true); got != jjN {
		t.Fatalf("jj dirtyPaths=%d, want %d (git would report %d - mutation to git branch over-counts)", got, jjN, gitN)
	}
	// End-to-end: RunStatus must take the jj branch via isJJWorkspace, not
	// only the unit helper with jj=true forced.
	r, err := RunStatus(context.Background(), http.DefaultClient, nil, "no stored credential", dir, "origin", "main")
	if err != nil {
		t.Fatalf("RunStatus: %v", err)
	}
	if !r.IsJJWorkspace {
		t.Fatal("expected IsJJWorkspace on a colocated checkout")
	}
	if r.DirtyPaths != jjN {
		t.Fatalf("RunStatus.DirtyPaths=%d, want jj count %d (got git-shaped %d?)", r.DirtyPaths, jjN, gitN)
	}
}

// A jj error must not print as "working tree: clean". Hide jj from PATH so
// runJJ fails with jj_not_found; the git fallback still sees the dirt.
func TestDirtyPathsJJErrorFallsBackToGitNotClean(t *testing.T) {
	requireJJ(t)
	dir := newColocatedJJRepo(t)
	jjCommitFile(t, dir, "README.md", "hi\n", "seed")
	writeTestFile(t, dir, "README.md", "dirty\n")
	writeTestFile(t, dir, "wip.txt", "uncommitted\n")

	// Confirm the jj path sees dirt before we hide the binary.
	if n := dirtyPaths(dir, true); n < 2 {
		t.Fatalf("precondition: want dirty tree via jj, got %d", n)
	}
	gitN := countLines(mustGitStatus(t, dir))
	if gitN < 2 {
		t.Fatalf("precondition: git must also see dirt for the fallback, got %d", gitN)
	}

	// Strip jj from PATH but keep git (and the rest of a minimal env).
	// requireJJ already rewrote HOME; only PATH changes here.
	t.Setenv("PATH", stripPathEntry(t, os.Getenv("PATH"), "jj"))
	if _, err := exec.LookPath("jj"); err == nil {
		t.Fatal("setup: jj still on PATH after strip; cannot exercise the error path")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Fatalf("setup: git must remain on PATH for the fallback: %v", err)
	}

	got := dirtyPaths(dir, true)
	if got == 0 {
		t.Fatal("jj error must not fail-open to 0 (status would say clean on a dirty tree)")
	}
	if got != gitN {
		t.Fatalf("jj-error fallback: dirtyPaths=%d, want git count %d", got, gitN)
	}

	// Human output must not claim clean either (PrintStatus keys on DirtyPaths).
	var b strings.Builder
	PrintStatus(&b, StatusReport{
		Dir: dir, Remote: "origin", TrunkRef: "main",
		IsJJWorkspace: true, DirtyPaths: got,
	})
	if strings.Contains(b.String(), "working tree: clean") {
		t.Fatalf("PrintStatus must not say clean when dirtyPaths fell back to %d:\n%s", got, b.String())
	}
	if !strings.Contains(b.String(), "uncommitted path") {
		t.Fatalf("PrintStatus should name uncommitted paths, got:\n%s", b.String())
	}
}

func mustGitStatus(t *testing.T, dir string) string {
	t.Helper()
	out, err := runGit(dir, "status", "--porcelain", "-uall")
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	return out
}

// stripPathEntry removes every PATH component whose base name is binName
// (so hiding `jj` does not require knowing which directory holds it).
func stripPathEntry(t *testing.T, pathEnv, binName string) string {
	t.Helper()
	var keep []string
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		// Drop the directory only when it is the one that resolves binName -
		// crude but enough: drop any component that contains an executable
		// named binName. Safer approach: rebuild PATH from LookPath's dir.
		candidate := filepath.Join(dir, binName)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			continue
		}
		keep = append(keep, dir)
	}
	// Ensure a known-good core PATH remains even if the ambient PATH was
	// mostly the jj install dir.
	out := strings.Join(keep, string(filepath.ListSeparator))
	if out == "" {
		out = "/usr/bin:/bin"
	} else {
		out = out + string(filepath.ListSeparator) + "/usr/bin:/bin"
	}
	return out
}
