package runkod

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

func TestSnapshotRefParts(t *testing.T) {
	cases := []struct {
		ref         string
		id, branch  string
		validBranch bool
	}{
		{"refs/workspaces/payments-fix/head", "payments-fix", "head", true},
		{"refs/workspaces/payments-fix/idea-b", "payments-fix", "idea-b", true},
		{"refs/workspaces/payments-fix/a/b", "payments-fix", "a/b", false}, // nested: rejected (§12.2)
		{"refs/workspaces/payments-fix/", "payments-fix", "", false},
		{"refs/workspaces/payments-fix/..evil", "payments-fix", "..evil", false},
		{"refs/heads/main", "", "", false},
	}
	for _, c := range cases {
		id, branch, ok := SnapshotRefParts(c.ref)
		if id != c.id || branch != c.branch || ok != c.validBranch {
			t.Errorf("SnapshotRefParts(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.ref, id, branch, ok, c.id, c.branch, c.validBranch)
		}
	}
}

// One workspace, N parallel lines of work: sibling branch refs pass the
// identical funnel treatment /head gets (registered-id gate included), a
// malformed branch segment is rejected outright, and the branch list the
// API serves is derived from the refs - never from the registry (§12.2
// workspace branches).
func TestWorkspaceBranchRefsThroughFunnelAndAPI(t *testing.T) {
	srv, bare, store := newWorkspaceTestServer(t)
	defer srv.Close()
	ctx := context.Background()

	resp := apiDo(t, srv, http.MethodPost, "/api/workspaces",
		`{"name": "payments-fix", "owner": "alice", "projects": ["checkout-api"]}`)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create workspace: %d", resp.StatusCode)
	}
	resp.Body.Close()

	// A freshly registered workspace has no refs and therefore no branches.
	var ws workspaceResponse
	resp = apiDo(t, srv, http.MethodGet, "/api/workspaces/payments-fix", "")
	if err := json.NewDecoder(resp.Body).Decode(&ws); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(ws.Branches) != 0 {
		t.Fatalf("no snapshots yet: want no branches, got %v", ws.Branches)
	}

	// Snapshot commits onto head and a parallel branch, via the funnel.
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/wip-a.go", "package checkout // line A\n")
	repo.Commit("runko workspace snapshot: line A")
	oldA, headA := pushCommit(t, repo, bare, "refs/workspaces/payments-fix/head")
	repo.WriteFile("commerce/checkout/wip-b.go", "package checkout // line B\n")
	repo.Commit("runko workspace snapshot: line B")
	_, headB := pushCommit(t, repo, bare, "refs/workspaces/payments-fix/idea-b")

	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	for _, u := range []RefUpdate{
		{OldSHA: oldA, NewSHA: headA, Ref: "refs/workspaces/payments-fix/head"},
		{OldSHA: oldA, NewSHA: headB, Ref: "refs/workspaces/payments-fix/idea-b"},
	} {
		if result := processor.Process(ctx, u, nil); !result.Accepted {
			t.Fatalf("branch snapshot push rejected: %+v", result)
		}
	}

	// Malformed branch segments are refused, not half-supported.
	bad := processor.Process(ctx, RefUpdate{OldSHA: oldA, NewSHA: headB, Ref: "refs/workspaces/payments-fix/a/b"}, nil)
	if bad.Accepted || !strings.Contains(bad.Message, "not a valid snapshot ref") {
		t.Fatalf("nested branch segment should be rejected with the naming message, got %+v", bad)
	}
	ghost := processor.Process(ctx, RefUpdate{OldSHA: oldA, NewSHA: headB, Ref: "refs/workspaces/ghost/idea-b"}, nil)
	if ghost.Accepted {
		t.Fatalf("unregistered workspace id must stay rejected on branch refs too")
	}

	// The API's branch list is exactly the refs.
	resp = apiDo(t, srv, http.MethodGet, "/api/workspaces/payments-fix", "")
	ws = workspaceResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&ws); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(ws.Branches) != 2 || ws.Branches[0] != "head" || ws.Branches[1] != "idea-b" {
		t.Fatalf("branches: want [head idea-b], got %v", ws.Branches)
	}
}
