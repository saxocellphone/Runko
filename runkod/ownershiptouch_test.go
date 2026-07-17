package runkod

import (
	"context"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// The classifyOwnershipTouch tests pin finding 2 of the 2026-07-16 dogfood
// review: the old any-OWNERS/PROJECT.yaml-path boolean made CanCreateProjects
// dead on arrival (every scaffold carries a manifest, so
// owners_modification_denied always fired first), while a rebase onto
// advanced trunk made snapshots false-positive on paths the agent never
// touched.

// seedAnchorTrunk gives trunk one existing project so the new-project cases
// have real "existing content" to be distinguished from.
func seedAnchorTrunk(t *testing.T, repo *gitfixture.Repo, bare string) string {
	t.Helper()
	repo.WriteFile("services/anchor/PROJECT.yaml", "schema: project/v1\nname: anchor\ntype: service\n")
	repo.WriteFile("services/anchor/inner/lib.go", "package inner\n")
	repo.Commit("initial")
	_, trunkSHA := pushCommit(t, repo, bare, "refs/heads/main")
	return trunkSHA
}

// newAgentWorkspaceProcessor wires the standard agent fixture: principal
// "bot" under the default policy, owning workspace agent-ws with affinity
// over services/.
func newAgentWorkspaceProcessor(t *testing.T, bare string) *Processor {
	t.Helper()
	store := NewMemStore()
	if _, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: "agent-ws", Owner: "bot", BaseRevision: "whatever",
		SnapshotRef: "refs/workspaces/agent-ws/head", Status: "active",
		WriteAllowlist: []string{"services"},
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	p := newTestProcessor(bare, store)
	p.Principals = []Principal{{Name: "bot", IsAgent: true, Policy: receive.DefaultAgentPolicy()}}
	return p
}

var agentPushEnv = []string{"REMOTE_USER=bot",
	"GIT_PUSH_OPTION_COUNT=1",
	"GIT_PUSH_OPTION_0=workspace=agent-ws"}

// TestAgentProjectCreateOnVirginPathsIsAccepted: a new manifest in a
// directory with no content at base is a project CREATE (CanCreateProjects,
// default true), not an owners modification.
func TestAgentProjectCreateOnVirginPathsIsAccepted(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	trunkSHA := seedAnchorTrunk(t, repo, bare)

	repo.WriteFile("services/newproj/PROJECT.yaml",
		"schema: project/v1\nname: newproj\ntype: service\nowners:\n  - alice\n")
	repo.WriteFile("services/newproj/main.go", "package main\n")
	repo.Commit("Create project newproj\n\nChange-Id: I1111111111111111111111111111111111111111")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	p := newAgentWorkspaceProcessor(t, bare)
	result := p.Process(context.Background(),
		RefUpdate{OldSHA: trunkSHA, NewSHA: headSHA, Ref: "refs/for/main"}, agentPushEnv)
	if !result.Accepted {
		t.Fatalf("a scaffold on virgin paths naming a human owner must pass: %+v", result)
	}
}

// TestAgentProjectCreateNamingItselfIsRefused: the same scaffold with the
// agent in owners: is a self-grant (§8.7's no-self-approval, at birth).
func TestAgentProjectCreateNamingItselfIsRefused(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	trunkSHA := seedAnchorTrunk(t, repo, bare)

	repo.WriteFile("services/newproj/PROJECT.yaml",
		"schema: project/v1\nname: newproj\ntype: service\nowners:\n  - bot\n")
	repo.Commit("Create project newproj\n\nChange-Id: I2222222222222222222222222222222222222222")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	p := newAgentWorkspaceProcessor(t, bare)
	result := p.Process(context.Background(),
		RefUpdate{OldSHA: trunkSHA, NewSHA: headSHA, Ref: "refs/for/main"}, agentPushEnv)
	if result.Accepted {
		t.Fatal("an agent naming itself owner of its new project must be refused")
	}
	if !strings.Contains(result.Message, "grants itself ownership") {
		t.Fatalf("expected the owner_self_grant refusal, got: %s", result.Message)
	}
}

// TestAgentTouchingExistingOwnershipIsRefused covers the three shapes that
// stay owners modifications: editing an existing manifest, introducing an
// OWNERS file, and carving a nested project out of existing content.
func TestAgentTouchingExistingOwnershipIsRefused(t *testing.T) {
	cases := []struct {
		name  string
		write func(repo *gitfixture.Repo)
	}{
		{"edit existing manifest", func(repo *gitfixture.Repo) {
			repo.WriteFile("services/anchor/PROJECT.yaml",
				"schema: project/v1\nname: anchor\ntype: service\nowners:\n  - alice\n")
		}},
		{"new OWNERS file", func(repo *gitfixture.Repo) {
			repo.WriteFile("services/anchor/OWNERS", "alice\n")
		}},
		{"nested manifest over existing content", func(repo *gitfixture.Repo) {
			repo.WriteFile("services/anchor/inner/PROJECT.yaml",
				"schema: project/v1\nname: inner\ntype: library\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bare := newBareRepo(t)
			repo := gitfixture.New(t)
			trunkSHA := seedAnchorTrunk(t, repo, bare)

			tc.write(repo)
			repo.Commit("touch ownership\n\nChange-Id: I3333333333333333333333333333333333333333")
			_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

			p := newAgentWorkspaceProcessor(t, bare)
			result := p.Process(context.Background(),
				RefUpdate{OldSHA: trunkSHA, NewSHA: headSHA, Ref: "refs/for/main"}, agentPushEnv)
			if result.Accepted {
				t.Fatal("expected the owners-modification refusal")
			}
			if !strings.Contains(result.Message, "does not allow modifying owners") {
				t.Fatalf("expected the owners-modification refusal, got: %s", result.Message)
			}
		})
	}
}

// TestSnapshotAfterTrunkOwnersDriftIsAccepted: the snapshot policy delta is
// judged against merge-base with trunk, never the snapshot ref's previous
// value - after a workspace syncs onto a trunk that changed OWNERS, the
// agent's next snapshot must not be blamed for that trunk-side change
// (found live, 2026-07-16).
func TestSnapshotAfterTrunkOwnersDriftIsAccepted(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("services/anchor/PROJECT.yaml", "schema: project/v1\nname: anchor\ntype: service\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	// Agent WIP on its own line, first snapshot (old == zero) accepted.
	repo.Run("checkout -q -b agent")
	repo.WriteFile("services/anchor/mine.go", "package anchor\n")
	repo.Commit("wip")
	firstOld, firstSHA := pushCommit(t, repo, bare, "refs/workspaces/agent-ws/head")
	if firstOld != zeroOID {
		t.Fatalf("fixture: expected a fresh snapshot ref, old = %s", firstOld)
	}

	p := newAgentWorkspaceProcessor(t, bare)
	result := p.Process(context.Background(),
		RefUpdate{OldSHA: firstOld, NewSHA: firstSHA, Ref: "refs/workspaces/agent-ws/head"},
		[]string{"REMOTE_USER=bot"})
	if !result.Accepted {
		t.Fatalf("baseline snapshot must pass: %+v", result)
	}

	// Trunk drifts: someone ELSE lands an OWNERS change.
	repo.Run("checkout -q main")
	repo.WriteFile("OWNERS", "human\n")
	repo.Commit("trunk owners change")
	pushCommit(t, repo, bare, "refs/heads/main")

	// The workspace syncs onto the new trunk and keeps working; its next
	// snapshot's old..new now contains the trunk-side OWNERS commit.
	repo.Run("checkout -q agent", "rebase -q main")
	repo.WriteFile("services/anchor/more.go", "package anchor\n")
	repo.Commit("more wip")
	oldSHA, newSHA := pushCommit(t, repo, bare, "refs/workspaces/agent-ws/head")
	if oldSHA != firstSHA {
		t.Fatalf("fixture: expected the previous snapshot as old, got %s", oldSHA)
	}

	result = p.Process(context.Background(),
		RefUpdate{OldSHA: oldSHA, NewSHA: newSHA, Ref: "refs/workspaces/agent-ws/head"},
		[]string{"REMOTE_USER=bot"})
	if !result.Accepted {
		t.Fatalf("snapshot blamed for a trunk-side owners change it never made: %+v", result)
	}
}
