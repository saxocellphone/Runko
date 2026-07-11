package runkod

import (
	"context"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

// singleUseFixture: a bare repo with trunk, a MemStore holding one AGENT-
// owned workspace ("bot-ws", owner "builder") and one human workspace
// ("human-ws", owner "alice"), and a Server carrying the agent principal.
func singleUseFixture(t *testing.T, flagOn bool) (*Server, *MemStore, *gitfixture.Repo, string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	ctx := context.Background()
	for id, owner := range map[string]string{"bot-ws": "builder", "human-ws": "alice"} {
		if _, err := store.CreateWorkspace(ctx, Workspace{
			ID: id, Owner: owner, SnapshotRef: "refs/workspaces/" + id + "/head", Status: "active",
		}); err != nil {
			t.Fatalf("CreateWorkspace %s: %v", id, err)
		}
	}
	srv := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store,
		SingleUseAgentWorkspaces: flagOn,
		Principals:               []Principal{{Name: "builder", IsAgent: true}},
	}
	return srv, store, repo, bare
}

// TestAgentWorkspaceClosesWhenLastOpenChangeConcludes drives the abandon
// path (land shares the same hook): two changes born in the agent
// workspace - concluding the first leaves it active (the task is still in
// flight), concluding the second closes it.
func TestAgentWorkspaceClosesWhenLastOpenChangeConcludes(t *testing.T) {
	srv, store, _, _ := singleUseFixture(t, true)
	ctx := context.Background()

	for _, key := range []string{"Ione", "Itwo"} {
		if _, err := store.CreateOrUpdateChange(ctx, key, "b", "h-"+key, "r", "t", "builder", "bot-ws", "head"); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
	}

	if _, apiErr := srv.abandonChangeCore(ctx, "Ione", nil); apiErr != nil {
		t.Fatalf("abandon Ione: %+v", apiErr)
	}
	ws, _, _ := store.GetWorkspace(ctx, "bot-ws")
	if ws.Status != "active" {
		t.Fatalf("one change still open - workspace must stay active, got %q", ws.Status)
	}

	if _, apiErr := srv.abandonChangeCore(ctx, "Itwo", nil); apiErr != nil {
		t.Fatalf("abandon Itwo: %+v", apiErr)
	}
	ws, _, _ = store.GetWorkspace(ctx, "bot-ws")
	if ws.Status != "closed" {
		t.Fatalf("last change concluded - agent workspace must close, got %q", ws.Status)
	}
}

// TestHumanWorkspaceNeverAutoCloses: the policy is agent-scoped (§8.7) -
// a human's long-lived workspace survives any number of conclusions.
func TestHumanWorkspaceNeverAutoCloses(t *testing.T) {
	srv, store, _, _ := singleUseFixture(t, true)
	ctx := context.Background()

	if _, err := store.CreateOrUpdateChange(ctx, "Ih", "b", "h", "r", "t", "alice", "human-ws", "head"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, apiErr := srv.abandonChangeCore(ctx, "Ih", nil); apiErr != nil {
		t.Fatalf("abandon: %+v", apiErr)
	}
	ws, _, _ := store.GetWorkspace(ctx, "human-ws")
	if ws.Status != "active" {
		t.Fatalf("human workspace must never auto-close, got %q", ws.Status)
	}
}

// TestSingleUseFlagOffKeepsAgentWorkspacesOpen pins the opt-out
// (--single-use-agent-workspaces=false).
func TestSingleUseFlagOffKeepsAgentWorkspacesOpen(t *testing.T) {
	srv, store, _, _ := singleUseFixture(t, false)
	ctx := context.Background()

	if _, err := store.CreateOrUpdateChange(ctx, "Ib", "b", "h", "r", "t", "builder", "bot-ws", "head"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, apiErr := srv.abandonChangeCore(ctx, "Ib", nil); apiErr != nil {
		t.Fatalf("abandon: %+v", apiErr)
	}
	ws, _, _ := store.GetWorkspace(ctx, "bot-ws")
	if ws.Status != "active" {
		t.Fatalf("flag off - workspace must stay active, got %q", ws.Status)
	}
}

// TestClosedWorkspaceRefusesPushes is the enforcement half: once closed,
// both write paths into the workspace - a change push claiming it as
// origin and a snapshot push - are refused at receive time with the
// create-a-fresh-workspace suggestion. Reuse becomes impossible, not
// merely discouraged.
func TestClosedWorkspaceRefusesPushes(t *testing.T) {
	srv, store, repo, bare := singleUseFixture(t, true)
	ctx := context.Background()
	if err := store.SetWorkspaceStatus(ctx, "bot-ws", "closed"); err != nil {
		t.Fatalf("close: %v", err)
	}
	p := newTestProcessor(bare, store)
	_ = srv

	// Change push claiming the closed workspace as origin.
	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("feature\n\nChange-Id: I0123456789012345678901234567890123456789")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")
	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, []string{
		"GIT_PUSH_OPTION_COUNT=1",
		"GIT_PUSH_OPTION_0=workspace=bot-ws",
	})
	if result.Accepted {
		t.Fatalf("a change push into a closed workspace must be refused")
	}
	if !strings.Contains(result.Message, "closed") || !strings.Contains(result.Message, "runko workspace create") {
		t.Fatalf("the refusal must teach the fix (fresh workspace), got %q", result.Message)
	}

	// Snapshot push to the closed workspace's ref.
	prevSHA, snapSHA := pushCommit(t, repo, bare, "refs/workspaces/bot-ws/head")
	result = p.Process(ctx, RefUpdate{OldSHA: prevSHA, NewSHA: snapSHA, Ref: "refs/workspaces/bot-ws/head"}, nil)
	if result.Accepted {
		t.Fatalf("a snapshot push into a closed workspace must be refused")
	}
	if !strings.Contains(result.Message, "closed") {
		t.Fatalf("snapshot refusal must name the closed state, got %q", result.Message)
	}

	// The human workspace (active) still takes snapshots - the refusal is
	// about status, not a blanket freeze.
	prevSHA2, snapSHA2 := pushCommit(t, repo, bare, "refs/workspaces/human-ws/head")
	if result := p.Process(ctx, RefUpdate{OldSHA: prevSHA2, NewSHA: snapSHA2, Ref: "refs/workspaces/human-ws/head"}, nil); !result.Accepted {
		t.Fatalf("active workspace snapshot must still pass: %+v", result)
	}
}
