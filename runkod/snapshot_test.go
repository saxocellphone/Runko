package runkod

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/receive"
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
