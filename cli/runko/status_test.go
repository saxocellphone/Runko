package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/checks"
)

const (
	statusTestIDReady   = "I1111111111111111111111111111111111111111"
	statusTestIDBlocked = "I2222222222222222222222222222222222222222"
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

// TestPrintStatusEmptyStackDirtyTreeStillDrawsTheGraph: nothing above
// trunk but an uncommitted working tree still has a seat for @ over ◆.
func TestPrintStatusEmptyStackDirtyTreeStillDrawsTheGraph(t *testing.T) {
	r := statusPrintFixture()
	r.Stack = []StackEntry{}
	r.DirtyPaths = 1
	var b strings.Builder
	PrintStatus(&b, r)
	out := b.String()
	if !strings.Contains(out, "@  1 uncommitted path(s)") || !strings.Contains(out, "◆  aaaabbbbcccc origin/main") {
		t.Fatalf("expected the @ working-tree node over the ◆ base:\n%s", out)
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
