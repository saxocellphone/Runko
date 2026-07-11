package runkod

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// newPrincipalTestServer is newApproveTestServer plus a principal registry
// (§15.1 interim, stage 12c) and a Change whose head was pushed by "alice"
// (REMOTE_USER forwarded in extraEnv, exactly as the hook does).
func newPrincipalTestServer(t *testing.T, principals []Principal) (srv *httptest.Server, changeID string, store Store) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store = NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store, Principals: principals}
	result := processor.Process(context.Background(),
		RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"},
		[]string{"REMOTE_USER=alice"})
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", Principals: principals}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv = httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, result.ChangeID, store
}

var testPrincipals = []Principal{
	{Name: "alice", Token: "alice-tok"},
	{Name: "bob", Token: "bob-tok"},
	{Name: "bumpbot", Token: "bot-tok", IsAgent: true, Policy: receive.DefaultAgentPolicy()},
}

func decodeClierr(t *testing.T, resp *http.Response) clierr.Error {
	t.Helper()
	defer resp.Body.Close()
	var ce clierr.Error
	if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return ce
}

// TestChangeRecordsAuthoredByFromRemoteUser: the funnel attributes the
// Change to the pushing principal (§7.5) - the substrate self-approval
// denial stands on.
func TestChangeRecordsAuthoredByFromRemoteUser(t *testing.T) {
	_, changeID, store := newPrincipalTestServer(t, testPrincipals)
	change, ok, err := store.GetChange(context.Background(), changeID)
	if err != nil || !ok {
		t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}
	if change.AuthoredBy != "alice" {
		t.Fatalf("expected AuthoredBy=alice from REMOTE_USER, got %q", change.AuthoredBy)
	}
}

// TestSelfApprovalDenied is §8.7's hard rule at the wire: the principal
// that pushed the Change's current head cannot approve it - not even as a
// legitimately required owner.
func TestSelfApprovalDenied(t *testing.T) {
	srv, changeID, _ := newPrincipalTestServer(t, testPrincipals)

	resp := postApprove(t, srv, changeID, "alice-tok", `{"owner_ref":"group:commerce-eng"}`)
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403 for self-approval, got %d: %s", resp.StatusCode, body)
	}
	if ce := decodeClierr(t, resp); ce.Code != "self_approval_denied" {
		t.Fatalf("expected self_approval_denied, got %+v", ce)
	}

	// A DIFFERENT human principal approves fine, attributed by token, no
	// approved_by needed in the body.
	resp = postApprove(t, srv, changeID, "bob-tok", `{"owner_ref":"group:commerce-eng"}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected bob's approval to succeed, got %d: %s", resp.StatusCode, body)
	}
}

// TestSelfApprovalDeniedForHonestDeployTokenCaller: the anonymous deploy
// token still names its approver as text; when that text IS the author,
// the same rule applies (a liar can still lie - that boundary is exactly
// what the named-token registry exists to retire, §15.1).
func TestSelfApprovalDeniedForHonestDeployTokenCaller(t *testing.T) {
	srv, changeID, _ := newPrincipalTestServer(t, testPrincipals)
	resp := postApprove(t, srv, changeID, "sekret", `{"owner_ref":"group:commerce-eng","approved_by":"alice"}`)
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d: %s", resp.StatusCode, body)
	}
}

// TestApprovedByMismatchRejected: a named principal cannot approve as
// somebody else.
func TestApprovedByMismatchRejected(t *testing.T) {
	srv, changeID, _ := newPrincipalTestServer(t, testPrincipals)
	resp := postApprove(t, srv, changeID, "bob-tok", `{"owner_ref":"group:commerce-eng","approved_by":"carol"}`)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
	}
	if ce := decodeClierr(t, resp); ce.Code != "approved_by_mismatch" {
		t.Fatalf("expected approved_by_mismatch, got %+v", ce)
	}
}

// TestAgentPrincipalCannotApprove pins §13.5's "Agent-only approval: No"
// as a hard rule, independent of any owner requirement.
func TestAgentPrincipalCannotApprove(t *testing.T) {
	srv, changeID, _ := newPrincipalTestServer(t, testPrincipals)
	resp := postApprove(t, srv, changeID, "bot-tok", `{"owner_ref":"group:commerce-eng"}`)
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d: %s", resp.StatusCode, body)
	}
	if ce := decodeClierr(t, resp); ce.Code != "agent_approval_denied" {
		t.Fatalf("expected agent_approval_denied, got %+v", ce)
	}
}

// TestAgentPolicyEnforcedAtReceive: the stage-6 AgentPolicy machinery,
// finally fed a principal (§8.7). The default policy requires workspace
// affinity, so an agent's direct refs/for push is refused outright; a
// policy without that requirement still enforces its path denylist.
func TestAgentPolicyEnforcedAtReceive(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("security/policy.yaml", "rules: []\n")
	repo.Commit("touch security\n\nChange-Id: Iaaaabbbbccccddddeeeeffff0000111122223333")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store,
		Principals: testPrincipals}

	// Default agent policy: refused for lacking workspace affinity.
	result := processor.Process(context.Background(),
		RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"},
		[]string{"REMOTE_USER=bumpbot"})
	if result.Accepted {
		t.Fatalf("expected the default agent policy to refuse a direct refs/for push (workspace affinity), got %+v", result)
	}
	if !strings.Contains(result.Message, "workspace") {
		t.Fatalf("expected a workspace-affinity violation message, got %q", result.Message)
	}

	// Affinity waived, but the §8.7 denylist still bites on security/**.
	relaxed := receive.DefaultAgentPolicy()
	relaxed.RequireWorkspaceAffinity = false
	processor.Principals = []Principal{{Name: "bumpbot", Token: "bot-tok", IsAgent: true, Policy: relaxed}}
	result = processor.Process(context.Background(),
		RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"},
		[]string{"REMOTE_USER=bumpbot"})
	if result.Accepted {
		t.Fatalf("expected the denylist to refuse security/**, got %+v", result)
	}
	if !strings.Contains(result.Message, "security/policy.yaml") {
		t.Fatalf("expected the denylisted path named, got %q", result.Message)
	}

	// The same push by a HUMAN principal (no agent policy) is accepted -
	// proving the refusals above came from the agent policy, not the path.
	result = processor.Process(context.Background(),
		RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"},
		[]string{"REMOTE_USER=alice"})
	if !result.Accepted {
		t.Fatalf("expected the human push to be accepted: %+v", result)
	}
}

// TestSnapshotOwnerOnlyPush closes 12b's documented gap: with principal
// identity at receive, refs/workspaces/<id>/* becomes owner-only. The
// anonymous deploy token still passes (it IS the everyone-credential,
// eval profile).
func TestSnapshotOwnerOnlyPush(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/wip.go", "package main // wip\n")
	repo.Commit("wip")
	_, snapSHA := pushCommit(t, repo, bare, "refs/workspaces/payments-fix/head")

	store := NewMemStore()
	if _, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: "payments-fix", Owner: "alice", BaseRevision: "whatever",
		ProjectAffinity: []string{"checkout-api"}, WriteAllowlist: []string{"commerce/checkout"},
		SnapshotRef: "refs/workspaces/payments-fix/head", Status: "active",
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store, Principals: testPrincipals}
	update := RefUpdate{OldSHA: zeroOID, NewSHA: snapSHA, Ref: "refs/workspaces/payments-fix/head"}

	if result := processor.Process(context.Background(), update, []string{"REMOTE_USER=bob"}); result.Accepted {
		t.Fatalf("expected bob's push to alice's workspace to be rejected, got %+v", result)
	} else if !strings.Contains(result.Message, "alice") {
		t.Fatalf("expected the owner named in the rejection, got %q", result.Message)
	}
	if result := processor.Process(context.Background(), update, []string{"REMOTE_USER=alice"}); !result.Accepted {
		t.Fatalf("expected the owner's push accepted, got %+v", result)
	}
	if result := processor.Process(context.Background(), update, nil); !result.Accepted {
		t.Fatalf("expected the anonymous deploy-token push accepted (eval profile), got %+v", result)
	}
}

// TestLandRecordsLandedBy: §7.5 attribution on the land verb.
func TestLandRecordsLandedBy(t *testing.T) {
	srv, changeID, store := newPrincipalTestServer(t, testPrincipals)

	// bob approves (the only gate this fixture resolves).
	resp := postApprove(t, srv, changeID, "bob-tok", `{"owner_ref":"group:commerce-eng"}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("approve: %d: %s", resp.StatusCode, body)
	}

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+changeID+"/land", nil)
	req.Header.Set("Authorization", "Bearer alice-tok")
	landResp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST land: %v", err)
	}
	defer landResp.Body.Close()
	if landResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(landResp.Body)
		t.Fatalf("land: %d: %s", landResp.StatusCode, body)
	}

	change, ok, err := store.GetChange(context.Background(), changeID)
	if err != nil || !ok {
		t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}
	if change.LandedBy != "alice" {
		t.Fatalf("expected LandedBy=alice, got %q", change.LandedBy)
	}
	if change.LandedAt.IsZero() {
		t.Fatalf("expected LandedAt to be recorded on land")
	}
}
