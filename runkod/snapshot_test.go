package runkod

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// markerScanner flags any file containing "SECRET" - a deterministic stand-in
// for gitleaks, same technique as receive's own funnel tests.
type markerScanner struct{}

func (markerScanner) Scan(files []receive.FileContent) ([]receive.SecretFinding, error) {
	for _, f := range files {
		if bytes.Contains(f.Content, []byte("SECRET")) {
			return []receive.SecretFinding{{Path: f.Path, Line: 1, RuleID: "marker", Description: "marker secret"}}, nil
		}
	}
	return nil, nil
}

// registerWorkspace seeds a registry row directly - these tests exercise the
// receive side, not the HTTP handler (workspace_test.go covers that).
func registerWorkspace(t *testing.T, store Store, id string) {
	t.Helper()
	_, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: id, Owner: "alice", BaseRevision: "whatever",
		SnapshotRef: "refs/workspaces/" + id + "/head", Status: "active",
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
}

// TestSnapshotPushToUnregisteredWorkspaceIsRejected closes the gap the 12b
// DAG row names: refs/workspaces/* used to pass the funnel unconditionally
// (pinned by the old TestProcessNonFunnelRefIsAcceptedUnconditionally, which
// now uses a tag ref instead). A snapshot ref without a registry row is
// rejected with a pointer at `runko workspace create`.
func TestSnapshotPushToUnregisteredWorkspaceIsRejected(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("wip.go", "package wip\n")
	repo.Commit("wip")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/workspaces/ghost/head")

	store := NewMemStore()
	p := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: markerScanner{}, Store: store}
	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/workspaces/ghost/head"}, nil)

	if result.Accepted {
		t.Fatalf("expected an unregistered workspace snapshot to be rejected, got %+v", result)
	}
	if !strings.Contains(result.Message, "ghost") || !strings.Contains(result.Message, "workspace create") {
		t.Fatalf("expected the rejection to name the workspace and the fix, got %q", result.Message)
	}
}

func TestSnapshotPushToRegisteredWorkspaceIsAccepted(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("wip.go", "package wip\n")
	repo.Commit("wip")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/workspaces/payments-fix/head")

	store := NewMemStore()
	registerWorkspace(t, store, "payments-fix")
	p := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: markerScanner{}, Store: store}
	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/workspaces/payments-fix/head"}, nil)

	if !result.Accepted {
		t.Fatalf("expected a registered workspace snapshot to be accepted, got %+v", result)
	}
	if result.ChangeID != "" {
		t.Fatalf("a snapshot must not create a Change, got %+v", result)
	}
}

// TestSnapshotPushWithSecretIsRejected is §12.2's "policy and secret scan
// apply BEFORE durability" - the entire reason snapshots go through the
// funnel at all: purging a secret from a pushed ref is a runbook (ref
// delete + reflog expire + prune), so the scan must run first.
func TestSnapshotPushWithSecretIsRejected(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("config.env", "TOKEN=SECRET-abc123\n")
	repo.Commit("wip with a secret")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/workspaces/payments-fix/head")

	store := NewMemStore()
	registerWorkspace(t, store, "payments-fix")
	p := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: markerScanner{}, Store: store}
	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/workspaces/payments-fix/head"}, nil)

	if result.Accepted {
		t.Fatalf("expected a snapshot containing a secret to be rejected, got %+v", result)
	}
	if !strings.Contains(result.Message, "config.env") {
		t.Fatalf("expected the rejection to name the offending file, got %q", result.Message)
	}
}

// TestSnapshotPushOverSizeCapIsRejected is §12.2's backstop against build
// artifacts entering snapshots when .gitignore hygiene fails.
func TestSnapshotPushOverSizeCapIsRejected(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("node_modules/dep/blob.js", strings.Repeat("x", 4096))
	repo.Commit("accidentally committed node_modules")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/workspaces/payments-fix/head")

	store := NewMemStore()
	registerWorkspace(t, store, "payments-fix")
	p := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: markerScanner{}, Store: store,
		MaxSnapshotDiffBytes: 1024}
	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/workspaces/payments-fix/head"}, nil)

	if result.Accepted {
		t.Fatalf("expected an oversized snapshot to be rejected, got %+v", result)
	}
	if !strings.Contains(result.Message, "cap") || !strings.Contains(result.Message, ".gitignore") {
		t.Fatalf("expected the rejection to explain the cap and the fix, got %q", result.Message)
	}

	// Negative disables the cap entirely.
	p.MaxSnapshotDiffBytes = -1
	result = p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/workspaces/payments-fix/head"}, nil)
	if !result.Accepted {
		t.Fatalf("expected the same push to pass with the cap disabled, got %+v", result)
	}
}

// TestAgentFirstSnapshotJudgedAgainstTrunkNotEmptyTree pins the fix for a
// bug found on the first real agent-token workspace run: a FIRST push to a
// fresh snapshot ref arrives with old == zero, and the funnel diffed it
// against the EMPTY TREE - so agent policy judged the agent as having
// authored the entire repository, and any file outside its affinity
// (here, someone else's project manifest already on trunk) violated. The
// snapshot's real delta is against trunk.
func TestAgentFirstSnapshotJudgedAgainstTrunkNotEmptyTree(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml", "schema: project/v1\nname: svc\ntype: service\n")
	repo.WriteFile("other/PROJECT.yaml", "schema: project/v1\nname: other\ntype: library\n")
	repo.Commit("trunk: two projects")
	pushCommit(t, repo, bare, "refs/heads/main")

	// The agent's actual work: one new file inside its affinity.
	repo.WriteFile("svc/wip.go", "package svc // agent wip\n")
	repo.Commit("agent wip")
	_, snapSHA := pushCommit(t, repo, bare, "refs/workspaces/agent-task/head")

	store := NewMemStore()
	if _, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: "agent-task", Owner: "bumpbot", BaseRevision: "whatever",
		ProjectAffinity: []string{"svc"}, WriteAllowlist: []string{"svc"},
		SnapshotRef: "refs/workspaces/agent-task/head", Status: "active",
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	agent := Principal{Name: "bumpbot", Token: "bot-tok", IsAgent: true, Policy: receive.DefaultAgentPolicy()}
	p := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store, Principals: []Principal{agent}}

	// First push: old == zero. Pre-fix this rejected with "other/
	// PROJECT.yaml is outside this workspace's project affinity".
	update := RefUpdate{OldSHA: zeroOID, NewSHA: snapSHA, Ref: "refs/workspaces/agent-task/head"}
	if result := p.Process(context.Background(), update, []string{"REMOTE_USER=bumpbot"}); !result.Accepted {
		t.Fatalf("agent's first in-affinity snapshot should be accepted, got %+v", result)
	}

	// An out-of-affinity edit in a LATER snapshot must still be caught -
	// the fix narrows the base, never the enforcement.
	repo.WriteFile("other/intrusion.go", "package other // out of lane\n")
	repo.Commit("agent oversteps")
	oldSHA, newSHA := pushCommit(t, repo, bare, "refs/workspaces/agent-task/head")
	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: newSHA, Ref: "refs/workspaces/agent-task/head"}, []string{"REMOTE_USER=bumpbot"})
	if result.Accepted {
		t.Fatalf("out-of-affinity snapshot edit should still be rejected, got %+v", result)
	}
	if !strings.Contains(result.Message, "other/intrusion.go") {
		t.Fatalf("rejection should name the offending path, got %q", result.Message)
	}

	// And a fresh-ref FIRST snapshot that ITSELF touches a foreign path
	// is also still caught (the merge-base delta includes it).
	repo2 := gitfixture.New(t)
	repo2.Run("fetch " + bare + " refs/heads/main")
	repo2.Run("checkout FETCH_HEAD")
	repo2.WriteFile("other/first-push-intrusion.go", "package other\n")
	repo2.Commit("first snapshot already out of lane")
	_, badSHA := pushCommit(t, repo2, bare, "refs/workspaces/agent-task-2/head")
	if _, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: "agent-task-2", Owner: "bumpbot", BaseRevision: "whatever",
		ProjectAffinity: []string{"svc"}, WriteAllowlist: []string{"svc"},
		SnapshotRef: "refs/workspaces/agent-task-2/head", Status: "active",
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	result = p.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: badSHA, Ref: "refs/workspaces/agent-task-2/head"}, []string{"REMOTE_USER=bumpbot"})
	if result.Accepted {
		t.Fatalf("first snapshot touching a foreign path should be rejected, got %+v", result)
	}
}

// TestAgentChangePushFromOwnWorkspaceSatisfiesAffinity pins the second
// half of the first real agent-token run's findings: agents could
// snapshot but never SUBMIT - a refs/for push was refused by
// RequireWorkspaceAffinity even when it declared (and the server
// validated) the agent's own workspace as its origin. A validated,
// owner-bound origin now carries the workspace's write allowlist as
// affinity; a bare push without options stays refused, and a push
// claiming someone ELSE'S workspace is rejected outright.
func TestAgentChangePushFromOwnWorkspaceSatisfiesAffinity(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("svc/PROJECT.yaml", "schema: project/v1\nname: svc\ntype: service\n")
	repo.Commit("trunk")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("svc/feature.go", "package svc // agent feature\n")
	repo.Commit("svc: agent feature\n\nChange-Id: I3333333333333333333333333333333333333333")
	oldSHA, newSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	if _, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: "agent-task", Owner: "bumpbot", BaseRevision: "whatever",
		ProjectAffinity: []string{"svc"}, WriteAllowlist: []string{"svc"},
		SnapshotRef: "refs/workspaces/agent-task/head", Status: "active",
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	agent := Principal{Name: "bumpbot", Token: "bot-tok", IsAgent: true, Policy: receive.DefaultAgentPolicy()}
	p := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store, Principals: []Principal{agent}}
	update := RefUpdate{OldSHA: oldSHA, NewSHA: newSHA, Ref: "refs/for/main"}
	env := func(extra ...string) []string { return append([]string{"REMOTE_USER=bumpbot"}, extra...) }

	// Bare push, no workspace claim: still refused (§8.7).
	if result := p.Process(context.Background(), update, env()); result.Accepted {
		t.Fatalf("bare agent refs/for push should still be refused, got %+v", result)
	}

	// Push from the agent's own workspace: affinity satisfied, accepted,
	// provenance recorded.
	result := p.Process(context.Background(), update, env(
		"GIT_PUSH_OPTION_COUNT=2", "GIT_PUSH_OPTION_0=workspace=agent-task", "GIT_PUSH_OPTION_1=workspace-branch=head"))
	if !result.Accepted {
		t.Fatalf("agent change push from its own workspace should be accepted, got %+v", result)
	}
	change, ok, err := store.GetChange(context.Background(), "I3333333333333333333333333333333333333333")
	if err != nil || !ok || change.OriginWorkspace != "agent-task" {
		t.Fatalf("change should record its workspace origin: %+v %v %v", change, ok, err)
	}

	// Someone else's workspace: rejected outright (provenance rule).
	if _, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: "not-yours", Owner: "alice", BaseRevision: "whatever",
		WriteAllowlist: []string{"svc"}, SnapshotRef: "refs/workspaces/not-yours/head", Status: "active",
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	result = p.Process(context.Background(), update, env(
		"GIT_PUSH_OPTION_COUNT=1", "GIT_PUSH_OPTION_0=workspace=not-yours"))
	if result.Accepted {
		t.Fatalf("claiming someone else's workspace should be rejected, got %+v", result)
	}

	// And the workspace's allowlist still constrains WHAT the agent may
	// touch: same push options, but the workspace only allows "other".
	if _, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: "wrong-lane", Owner: "bumpbot", BaseRevision: "whatever",
		WriteAllowlist: []string{"other"}, SnapshotRef: "refs/workspaces/wrong-lane/head", Status: "active",
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	result = p.Process(context.Background(), update, env(
		"GIT_PUSH_OPTION_COUNT=1", "GIT_PUSH_OPTION_0=workspace=wrong-lane"))
	if result.Accepted {
		t.Fatalf("push touching paths outside the claimed workspace's allowlist should be refused, got %+v", result)
	}
}

// TestSnapshotPushRecordsWorkspaceEventAndPokesBus pins the §12.6 receive
// hook: an accepted snapshot writes exactly one stats row (numstat totals,
// actor, branch) and pokes a live subscriber; the ref keeps producing no
// Change row (asserted above).
func TestSnapshotPushRecordsWorkspaceEventAndPokesBus(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("wip.go", "package wip\n\nfunc WIP() {}\n")
	repo.Commit("wip")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/workspaces/payments-fix/feature")

	store := NewMemStore()
	registerWorkspace(t, store, "payments-fix")
	bus := NewEventBus()
	sub, cancel := bus.Subscribe("payments-fix")
	defer cancel()
	p := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: markerScanner{}, Store: store, Events: bus}

	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/workspaces/payments-fix/feature"},
		[]string{"REMOTE_USER=alice"})
	if !result.Accepted {
		t.Fatalf("expected the snapshot accepted, got %+v", result)
	}

	evs, err := store.ListWorkspaceEvents(context.Background(), "payments-fix", 0, 0)
	if err != nil || len(evs) != 1 {
		t.Fatalf("expected exactly one workspace event, got %+v err=%v", evs, err)
	}
	ev := evs[0]
	if ev.Type != WorkspaceEventSnapshotPushed || ev.Branch != "feature" || ev.Actor != "alice" || ev.SHA != headSHA {
		t.Fatalf("unexpected event: %+v", ev)
	}
	if ev.FilesChanged != 1 || ev.Additions == 0 || ev.Deletions != 0 {
		t.Fatalf("expected numstat totals for one added file, got %+v", ev)
	}

	select {
	case <-sub.Ready():
	default:
		t.Fatalf("expected the bus poked on an accepted snapshot")
	}
	if poked, ok := sub.Take(); !ok || poked.ID != ev.ID {
		t.Fatalf("expected the recorded event on the bus, got %+v ok=%v", poked, ok)
	}
}

// TestRejectedSnapshotRecordsNoWorkspaceEvent - a refused push never
// reaches the timeline (the row is written on the ACCEPT branch only).
func TestRejectedSnapshotRecordsNoWorkspaceEvent(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("config.env", "TOKEN=SECRET-abc123\n")
	repo.Commit("wip with a secret")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/workspaces/payments-fix/head")

	store := NewMemStore()
	registerWorkspace(t, store, "payments-fix")
	p := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: markerScanner{}, Store: store, Events: NewEventBus()}
	if result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/workspaces/payments-fix/head"}, nil); result.Accepted {
		t.Fatalf("expected the secret snapshot rejected, got %+v", result)
	}
	if evs, _ := store.ListWorkspaceEvents(context.Background(), "payments-fix", 0, 0); len(evs) != 0 {
		t.Fatalf("a rejected snapshot must record nothing, got %+v", evs)
	}
}
