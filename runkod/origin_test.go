package runkod

// §12.2's workspace-branch ↔ stack mapping (decided 2026-07-08): a magic-ref
// push may declare which workspace branch it came from via git push options
// (stamped by `runko change push` from the worktree's runko.workspace/
// runko.branch config, forwarded to the funnel as GIT_PUSH_OPTION_* env).
// The funnel validates the claim against the registry - a Change pinned to
// the wrong (or someone else's) stack in every view would be worse than no
// provenance at all - and records it on the Change.

import (
	"context"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

func TestPushOptionsParseInOrder(t *testing.T) {
	opts := pushOptions([]string{
		"GIT_PUSH_OPTION_COUNT=3",
		"GIT_PUSH_OPTION_1=b",
		"GIT_PUSH_OPTION_0=a",
		"GIT_PUSH_OPTION_2=c=with=equals",
		"REMOTE_USER=alice", // unrelated env must be ignored
	})
	want := []string{"a", "b", "c=with=equals"}
	if len(opts) != len(want) {
		t.Fatalf("pushOptions = %v, want %v", opts, want)
	}
	for i := range want {
		if opts[i] != want[i] {
			t.Fatalf("pushOptions = %v, want %v", opts, want)
		}
	}
	if got := pushOptions([]string{"REMOTE_USER=alice"}); len(got) != 0 {
		t.Fatalf("no options env should parse to none, got %v", got)
	}
}

// originFixture seeds trunk, registers a workspace owned by alice, and
// returns everything a magic-ref origin push needs.
func originFixture(t *testing.T) (p *Processor, store *MemStore, repo *gitfixture.Repo, bare string) {
	t.Helper()
	bare = newBareRepo(t)
	repo = gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store = NewMemStore()
	if _, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: "checkout-fixes", Owner: "alice", SnapshotRef: "refs/workspaces/checkout-fixes/head", Status: "active",
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return newTestProcessor(bare, store), store, repo, bare
}

func TestPushOptionOriginRecordedAndPreservedAcrossPlainAmend(t *testing.T) {
	p, store, repo, bare := originFixture(t)
	ctx := context.Background()

	repo.WriteFile("feature.txt", "v1\n")
	// A stable Change-Id trailer so the amend below updates the SAME
	// Change (without it, the funnel seeds a fresh id from each new SHA).
	repo.Commit("add a feature\n\nChange-Id: I0123456789012345678901234567890123456789")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	withOrigin := []string{
		"GIT_PUSH_OPTION_COUNT=2",
		"GIT_PUSH_OPTION_0=workspace=checkout-fixes",
		"GIT_PUSH_OPTION_1=workspace-branch=perf-experiment",
	}
	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, withOrigin)
	if !result.Accepted {
		t.Fatalf("origin push should be accepted: %+v", result)
	}
	change, _, _ := store.GetChange(ctx, result.ChangeID)
	if change.OriginWorkspace != "checkout-fixes" || change.OriginBranch != "perf-experiment" {
		t.Fatalf("origin not recorded: %+v", change)
	}

	// An amend pushed WITHOUT options (plain git, a different machine) must
	// PRESERVE the recorded origin, not erase it.
	repo.WriteFile("feature.txt", "v2\n")
	repo.Run("add -A", "commit --amend --no-edit")
	prevSHA := headSHA
	_, amendSHA := pushCommit(t, repo, bare, "refs/for/main")
	result = p.Process(ctx, RefUpdate{OldSHA: prevSHA, NewSHA: amendSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("plain amend should be accepted: %+v", result)
	}
	change, _, _ = store.GetChange(ctx, result.ChangeID)
	if change.OriginWorkspace != "checkout-fixes" || change.OriginBranch != "perf-experiment" {
		t.Fatalf("plain amend must preserve origin, got %+v", change)
	}
}

func TestPushOptionOriginDefaultsToHeadBranch(t *testing.T) {
	p, store, repo, bare := originFixture(t)
	ctx := context.Background()

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add a feature")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, []string{
		"GIT_PUSH_OPTION_COUNT=1",
		"GIT_PUSH_OPTION_0=workspace=checkout-fixes",
	})
	if !result.Accepted {
		t.Fatalf("push should be accepted: %+v", result)
	}
	change, _, _ := store.GetChange(ctx, result.ChangeID)
	if change.OriginBranch != "head" {
		t.Fatalf("branch should default to head (§12.2), got %q", change.OriginBranch)
	}
}

func TestPushOptionUnknownWorkspaceRejected(t *testing.T) {
	p, _, repo, bare := originFixture(t)

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add a feature")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, []string{
		"GIT_PUSH_OPTION_COUNT=1",
		"GIT_PUSH_OPTION_0=workspace=ghost",
	})
	if result.Accepted {
		t.Fatalf("a push claiming an unregistered workspace must be rejected")
	}
	if !strings.Contains(result.Message, "ghost") || !strings.Contains(result.Message, "runko workspace attach") {
		t.Fatalf("rejection must name the workspace and the fix, got %q", result.Message)
	}
}

func TestPushOptionForeignWorkspaceRejectedForNamedPrincipal(t *testing.T) {
	p, store, repo, bare := originFixture(t)
	ctx := context.Background()

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add a feature")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	claim := []string{
		"GIT_PUSH_OPTION_COUNT=1",
		"GIT_PUSH_OPTION_0=workspace=checkout-fixes",
	}

	// bob claiming alice's workspace: rejected, naming the real owner.
	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"},
		append([]string{"REMOTE_USER=bob"}, claim...))
	if result.Accepted {
		t.Fatalf("a named principal must not claim someone else's workspace as origin")
	}
	if !strings.Contains(result.Message, "alice") {
		t.Fatalf("rejection must name the actual owner, got %q", result.Message)
	}

	// alice claiming her own: accepted, origin recorded.
	result = p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"},
		append([]string{"REMOTE_USER=alice"}, claim...))
	if !result.Accepted {
		t.Fatalf("the owner's own claim should be accepted: %+v", result)
	}
	change, _, _ := store.GetChange(ctx, result.ChangeID)
	if change.OriginWorkspace != "checkout-fixes" {
		t.Fatalf("origin not recorded for the owner: %+v", change)
	}
}

// TestChangesAreBornInWorkspaces pins the 2026-07-09 decision superseding
// "recorded provenance, never an identity constraint": with
// RequireChangeWorkspace set (the production default), a refs/for push
// with no validated workspace origin is refused - for EVERYONE, human and
// agent alike. Exemptions are structural, not principal-based: a
// brand-new monorepo's bootstrap push (unborn trunk - workspaces need a
// base revision, so requiring one first would deadlock every new org).
func TestChangesAreBornInWorkspaces(t *testing.T) {
	p, _, repo, bare := originFixture(t)
	p.RequireChangeWorkspace = true
	ctx := context.Background()

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add a feature\n\nChange-Id: I9999999999999999999999999999999999999999")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")
	update := RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}

	// No workspace claim: refused, with the fix named.
	result := p.Process(ctx, update, nil)
	if result.Accepted {
		t.Fatalf("workspaceless change push should be refused, got %+v", result)
	}
	if !strings.Contains(result.Message, "workspace") || !strings.Contains(result.Message, "runko workspace create") {
		t.Fatalf("rejection should teach the workspace flow, got %q", result.Message)
	}

	// The same push claiming alice's registered workspace: accepted, with
	// origin recorded.
	result = p.Process(ctx, update, []string{
		"GIT_PUSH_OPTION_COUNT=1", "GIT_PUSH_OPTION_0=workspace=checkout-fixes",
	})
	if !result.Accepted {
		t.Fatalf("workspace-origin push should be accepted, got %+v", result)
	}

	// Snapshot refs are untouched by this gate (they ARE the workspace
	// write) - and so is the flag-off posture.
	p.RequireChangeWorkspace = false
	if result := p.Process(ctx, update, nil); !result.Accepted {
		t.Fatalf("flag off should restore the old behavior, got %+v", result)
	}
}

// TestBootstrapPushExemptFromWorkspaceRequirement: an empty monorepo's
// first change can never have a workspace (workspaces need a base
// revision), so the gate must let the bootstrap through.
func TestBootstrapPushExemptFromWorkspaceRequirement(t *testing.T) {
	bare := newBareRepo(t) // unborn trunk - no refs at all
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml", "schema: project/v1\nname: svc\ntype: service\n")
	repo.Commit("bootstrap\n\nChange-Id: I8888888888888888888888888888888888888888")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	p := newTestProcessor(bare, NewMemStore())
	p.RequireChangeWorkspace = true
	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("bootstrap push onto an unborn trunk must stay exempt, got %+v", result)
	}
}

// TestOneStackPerWorkspaceBranch pins §12.2's branch ↔ stack mapping as an
// invariant (2026-07-09): a workspace branch carries at most ONE stack.
// Amends, restacks, and grows chain through the open changes and pass; an
// unrelated trunk-based line claiming the same branch is refused (observed
// live: two agents sharing one owner account made one branch render as two
// disagreeing stacks). A parallel branch or a post-land fresh start stays
// legal.
func TestOneStackPerWorkspaceBranch(t *testing.T) {
	p, store, repo, bare := originFixture(t)
	ctx := context.Background()
	claim := func(branch string) []string {
		return []string{
			"GIT_PUSH_OPTION_COUNT=2",
			"GIT_PUSH_OPTION_0=workspace=checkout-fixes",
			"GIT_PUSH_OPTION_1=workspace-branch=" + branch,
		}
	}

	// Change A opens the branch's stack.
	repo.WriteFile("a.txt", "a\n")
	repo.Commit("change A\n\nChange-Id: Iaaaa111111111111111111111111111111111111")
	oldA, headA := pushCommit(t, repo, bare, "refs/for/main")
	if r := p.Process(ctx, RefUpdate{OldSHA: oldA, NewSHA: headA, Ref: "refs/for/main"}, claim("head")); !r.Accepted {
		t.Fatalf("A should open the stack: %+v", r)
	}

	// An UNRELATED trunk-based change claiming the same branch: refused,
	// naming the open change and the ways out.
	repo2 := gitfixture.New(t)
	repo2.Run("fetch "+bare+" refs/heads/main", "checkout FETCH_HEAD")
	repo2.WriteFile("b.txt", "b\n")
	repo2.Commit("change B\n\nChange-Id: Ibbbb111111111111111111111111111111111111")
	_, headB := pushCommit(t, repo2, bare, "refs/for/main")
	r := p.Process(ctx, RefUpdate{OldSHA: headA, NewSHA: headB, Ref: "refs/for/main"}, claim("head"))
	if r.Accepted {
		t.Fatalf("second unrelated stack on one branch should be refused, got %+v", r)
	}
	if !strings.Contains(r.Message, "Iaaaa111111111111111111111111111111111111") || !strings.Contains(r.Message, "one branch, one stack") {
		t.Fatalf("rejection should name the open change and the invariant, got %q", r.Message)
	}

	// The same commit on a PARALLEL branch is fine - branches are the
	// unit of parallel work.
	if r := p.Process(ctx, RefUpdate{OldSHA: headA, NewSHA: headB, Ref: "refs/for/main"}, claim("side")); !r.Accepted {
		t.Fatalf("parallel branch should accept an independent line: %+v", r)
	}

	// Growing the head stack (A <- C, tip includes A) passes.
	repo.WriteFile("c.txt", "c\n")
	repo.Commit("change C\n\nChange-Id: Icccc111111111111111111111111111111111111")
	_, headC := pushCommit(t, repo, bare, "refs/for/main")
	if r := p.Process(ctx, RefUpdate{OldSHA: headB, NewSHA: headC, Ref: "refs/for/main"}, claim("head")); !r.Accepted {
		t.Fatalf("growing the branch's stack should pass: %+v", r)
	}

	// Amending the tip (same ids) passes.
	repo.WriteFile("c.txt", "c2\n")
	repo.Run("add -A", "commit --amend --no-edit")
	_, headC2 := pushCommit(t, repo, bare, "refs/for/main")
	if r := p.Process(ctx, RefUpdate{OldSHA: headC, NewSHA: headC2, Ref: "refs/for/main"}, claim("head")); !r.Accepted {
		t.Fatalf("amending the stack should pass: %+v", r)
	}

	// Pushing a NON-tip member alone (dropping C from the series) is the
	// subtle foot-gun: refused, since C's open change would fall out of
	// the chain.
	r = p.Process(ctx, RefUpdate{OldSHA: headC2, NewSHA: headA, Ref: "refs/for/main"}, claim("head"))
	if r.Accepted {
		t.Fatalf("pushing a non-tip member alone should be refused: %+v", r)
	}

	// Once the branch's changes land/abandon, a fresh line is welcome.
	for _, id := range []string{"Iaaaa111111111111111111111111111111111111", "Icccc111111111111111111111111111111111111"} {
		if _, err := store.MarkChangeAbandoned(ctx, id); err != nil {
			t.Fatalf("abandon %s: %v", id, err)
		}
	}
	repo3 := gitfixture.New(t)
	repo3.Run("fetch "+bare+" refs/heads/main", "checkout FETCH_HEAD")
	repo3.WriteFile("d.txt", "d\n")
	repo3.Commit("change D\n\nChange-Id: Idddd111111111111111111111111111111111111")
	_, headD := pushCommit(t, repo3, bare, "refs/for/main")
	if r := p.Process(ctx, RefUpdate{OldSHA: headC2, NewSHA: headD, Ref: "refs/for/main"}, claim("head")); !r.Accepted {
		t.Fatalf("fresh stack after the old one closed should pass: %+v", r)
	}
}

// The §12.6 golden-path nudge: a workspace's FIRST change push that
// streamed nothing earns one advisory remote: block; anything that
// streamed - or any repeat push - stays quiet.
func TestFirstUnstreamedChangePushGetsStreamingNudge(t *testing.T) {
	p, _, repo, bare := originFixture(t)
	ctx := context.Background()

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add a feature\n\nChange-Id: I5123456789012345678901234567890123456789")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	opts := []string{"GIT_PUSH_OPTION_COUNT=1", "GIT_PUSH_OPTION_0=workspace=checkout-fixes"}
	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, opts)
	if !result.Accepted {
		t.Fatalf("push should be accepted: %+v", result)
	}
	if !strings.Contains(result.Message, "runko workspace watch") ||
		!strings.Contains(result.Message, "runko agent hooks --install") {
		t.Fatalf("first unstreamed push should carry the streaming nudge, got:\n%s", result.Message)
	}

	// The second push (an amend) is not a first push - no nag loop.
	repo.WriteFile("feature.txt", "v2\n")
	repo.Run("add -A", "commit --amend --no-edit")
	prevSHA := headSHA
	_, amendSHA := pushCommit(t, repo, bare, "refs/for/main")
	result = p.Process(ctx, RefUpdate{OldSHA: prevSHA, NewSHA: amendSHA, Ref: "refs/for/main"}, opts)
	if !result.Accepted {
		t.Fatalf("amend push should be accepted: %+v", result)
	}
	if strings.Contains(result.Message, "runko workspace watch") {
		t.Fatalf("a repeat push must not nudge, got:\n%s", result.Message)
	}
}

// One snapshot_pushed row is (usually) the push's own auto-snapshot from
// moments earlier - it must not count as "streamed".
func TestAutoSnapshotAloneDoesNotSuppressTheNudge(t *testing.T) {
	p, store, repo, bare := originFixture(t)
	ctx := context.Background()

	if _, err := store.RecordWorkspaceEvent(ctx, WorkspaceEvent{
		Type: WorkspaceEventSnapshotPushed, WorkspaceID: "checkout-fixes", Branch: "head", Actor: "alice",
	}); err != nil {
		t.Fatalf("seed snapshot event: %v", err)
	}

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add a feature\n\nChange-Id: I6123456789012345678901234567890123456789")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")
	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"},
		[]string{"GIT_PUSH_OPTION_COUNT=1", "GIT_PUSH_OPTION_0=workspace=checkout-fixes"})
	if !result.Accepted {
		t.Fatalf("push should be accepted: %+v", result)
	}
	if !strings.Contains(result.Message, "runko workspace watch") {
		t.Fatalf("one auto-snapshot must not suppress the nudge, got:\n%s", result.Message)
	}
}

// A snapshot cadence (two+ events) or any activity row means the
// workspace streamed - the nudge stays quiet.
func TestStreamedWorkspaceGetsNoNudge(t *testing.T) {
	ctx := context.Background()

	t.Run("snapshot cadence", func(t *testing.T) {
		p, store, repo, bare := originFixture(t)
		for range 2 {
			if _, err := store.RecordWorkspaceEvent(ctx, WorkspaceEvent{
				Type: WorkspaceEventSnapshotPushed, WorkspaceID: "checkout-fixes", Branch: "head", Actor: "alice",
			}); err != nil {
				t.Fatalf("seed snapshot event: %v", err)
			}
		}
		repo.WriteFile("feature.txt", "v1\n")
		repo.Commit("add a feature\n\nChange-Id: I7123456789012345678901234567890123456789")
		oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")
		result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"},
			[]string{"GIT_PUSH_OPTION_COUNT=1", "GIT_PUSH_OPTION_0=workspace=checkout-fixes"})
		if !result.Accepted {
			t.Fatalf("push should be accepted: %+v", result)
		}
		if strings.Contains(result.Message, "runko workspace watch") {
			t.Fatalf("a snapshotting workspace must not nudge, got:\n%s", result.Message)
		}
	})

	t.Run("activity rows", func(t *testing.T) {
		p, store, repo, bare := originFixture(t)
		if _, err := store.RecordWorkspaceActivity(ctx, []WorkspaceActivity{
			{WorkspaceID: "checkout-fixes", Actor: "alice", Kind: "edit", Detail: "feature.txt"},
		}); err != nil {
			t.Fatalf("seed activity: %v", err)
		}
		repo.WriteFile("feature.txt", "v1\n")
		repo.Commit("add a feature\n\nChange-Id: I8123456789012345678901234567890123456789")
		oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")
		result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"},
			[]string{"GIT_PUSH_OPTION_COUNT=1", "GIT_PUSH_OPTION_0=workspace=checkout-fixes"})
		if !result.Accepted {
			t.Fatalf("push should be accepted: %+v", result)
		}
		if strings.Contains(result.Message, "runko workspace watch") {
			t.Fatalf("a hooks-wired workspace must not nudge, got:\n%s", result.Message)
		}
	})
}
