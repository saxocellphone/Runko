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
