package runkod

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// newDescriptionGateServer seeds an open change authored by `author` (an
// agent when isAgent) with NO §8.6 description, on an unpoliced-but-ALLOWED
// server so the §8.7 description gate is the only thing that can block the
// land. The author's policy carries RequireDescription and nothing else, so
// the seed push itself isn't gated on affinity/caps - these tests isolate
// the merge-time description gate.
func newDescriptionGateServer(t *testing.T, author string, isAgent bool) (*httptest.Server, *Server, string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", unpolicedManifest)
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	pr := Principal{Name: author, IsAgent: isAgent, Policy: receive.AgentPolicy{RequireDescription: true}}
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store, Principals: []Principal{pr}}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, []string{"REMOTE_USER=" + author})
	if !result.Accepted {
		t.Fatalf("seed push rejected: %+v", result)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", AllowUnpolicedLand: true, Principals: []Principal{pr}}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return httptest.NewServer(handler), server, result.ChangeID
}

func hasBlocker(blockers []string, substr string) bool {
	for _, b := range blockers {
		if strings.Contains(b, substr) {
			return true
		}
	}
	return false
}

// TestAgentChangeNotMergeableWithoutDescription: the §8.7 gate - an
// agent-authored open change with an empty §8.6 description is not mergeable,
// with a blocker naming the fix (`runko change describe`).
func TestAgentChangeNotMergeableWithoutDescription(t *testing.T) {
	hs, _, changeID := newDescriptionGateServer(t, "builder", true)
	defer hs.Close()

	reqs := getMergeRequirements(t, hs, changeID)
	if reqs.Mergeable {
		t.Fatalf("an agent change with no description must not be mergeable: %+v", reqs)
	}
	if !hasBlocker(reqs.Blockers, "no description") || !hasBlocker(reqs.Blockers, "runko change describe") {
		t.Fatalf("expected the description blocker naming the fix, got %v", reqs.Blockers)
	}
}

// TestAgentChangeMergeableAfterDescribe: setting the §8.6 description clears
// the gate - the change becomes mergeable (nothing else blocks it).
func TestAgentChangeMergeableAfterDescribe(t *testing.T) {
	hs, srv, changeID := newDescriptionGateServer(t, "builder", true)
	defer hs.Close()

	if _, apiErr := srv.describeChangeCore(context.Background(), changeID,
		strPtr("Adds the checkout entrypoint; verified with make check."), nil); apiErr != nil {
		t.Fatalf("describe: %+v", apiErr)
	}
	reqs := getMergeRequirements(t, hs, changeID)
	if hasBlocker(reqs.Blockers, "no description") {
		t.Fatalf("description set - the blocker must be gone: %v", reqs.Blockers)
	}
	if !reqs.Mergeable {
		t.Fatalf("with a description and no other gate, the change must be mergeable: %+v", reqs)
	}
}

// TestHumanChangeExemptFromDescriptionGate: RequireDescription is an
// AgentPolicy gate - a non-agent author is untouched, even with an empty
// description.
func TestHumanChangeExemptFromDescriptionGate(t *testing.T) {
	hs, _, changeID := newDescriptionGateServer(t, "alice", false)
	defer hs.Close()

	reqs := getMergeRequirements(t, hs, changeID)
	if hasBlocker(reqs.Blockers, "no description") {
		t.Fatalf("a human change must be exempt from the description gate: %v", reqs.Blockers)
	}
	if !reqs.Mergeable {
		t.Fatalf("a human unpoliced-but-allowed change must be mergeable: %+v", reqs)
	}
}
