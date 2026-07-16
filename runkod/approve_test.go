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
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/receive"
)

// newApproveTestServer seeds two owned projects where billing-lib DECLARES A
// DEPENDENCY on checkout-api, and one open Change touching only
// commerce/checkout - so the affected closure is {checkout-api, billing-lib}
// but the touched-paths set is {checkout-api} only. That asymmetry is the
// §7.3 rule the owners gate must honor: billing-lib's tests run (it's in the
// closure), but its owners had no code touched and get no approval veto.
func newApproveTestServer(t *testing.T) (srv *httptest.Server, bare string, changeID string, store Store) {
	t.Helper()
	bare = newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\n")
	repo.WriteFile("libs/billing/PROJECT.yaml",
		"schema: project/v1\nname: billing-lib\ntype: library\nowners:\n  - group:billing-eng\ndependencies:\n  - checkout-api\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store = NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return httptest.NewServer(handler), bare, result.ChangeID, store
}

func postApprove(t *testing.T, srv *httptest.Server, changeID, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+changeID+"/approve", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST approve: %v", err)
	}
	return resp
}

func getMergeRequirements(t *testing.T, srv *httptest.Server, changeID string) checks.MergeRequirements {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/changes/"+changeID+"/merge-requirements", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET merge-requirements: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET merge-requirements: %d: %s", resp.StatusCode, body)
	}
	var reqs checks.MergeRequirements
	if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
		t.Fatalf("decode merge requirements: %v", err)
	}
	return reqs
}

// TestOwnerRequirementsComeFromTouchedPathsNotClosure is §7.3's rule at the
// wire: the Change touches only commerce/checkout, so exactly checkout-api's
// owner is required - billing-lib is in the affected closure (its declared
// dependency makes its tests run) but its owner gets no approval veto.
func TestOwnerRequirementsComeFromTouchedPathsNotClosure(t *testing.T) {
	srv, _, changeID, _ := newApproveTestServer(t)
	defer srv.Close()

	reqs := getMergeRequirements(t, srv, changeID)
	if len(reqs.RequiredOwners) != 1 || reqs.RequiredOwners[0] != "group:commerce-eng" {
		t.Fatalf("required owners: want exactly [group:commerce-eng], got %v", reqs.RequiredOwners)
	}
	if len(reqs.OutstandingOwners) != 1 || reqs.OutstandingOwners[0] != "group:commerce-eng" {
		t.Fatalf("outstanding owners: want [group:commerce-eng], got %v", reqs.OutstandingOwners)
	}
	if reqs.Mergeable {
		t.Fatalf("expected not mergeable while an owner approval is outstanding, got %+v", reqs)
	}
}

// TestApproveUnblocksLand is the §28.3 stage 11c owners bar end to end at
// the handler level: land refused while the owner approval is outstanding,
// then a real POST .../approve flips the same Mergeable bool the land gate
// reads, and the land succeeds.
func TestApproveUnblocksLand(t *testing.T) {
	srv, bare, changeID, _ := newApproveTestServer(t)
	defer srv.Close()

	resp := postLand(t, srv, changeID, "sekret")
	if resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 409 while approval outstanding, got %d: %s", resp.StatusCode, body)
	}
	var ce clierr.Error
	if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil {
		t.Fatalf("decode 409 body: %v", err)
	}
	if ce.Code != "not_mergeable" {
		t.Fatalf("expected not_mergeable, got %+v", ce)
	}

	resp = postApprove(t, srv, changeID, "sekret",
		`{"owner_ref": "group:commerce-eng", "approved_by": "alice"}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 on approve, got %d: %s", resp.StatusCode, body)
	}
	var reqs checks.MergeRequirements
	if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
		t.Fatalf("decode approve response: %v", err)
	}
	if !reqs.Mergeable {
		t.Fatalf("expected mergeable after the only required approval, got %+v", reqs)
	}
	if len(reqs.SatisfiedOwners) != 1 || reqs.SatisfiedOwners[0] != "group:commerce-eng" {
		t.Fatalf("satisfied owners: want [group:commerce-eng], got %v", reqs.SatisfiedOwners)
	}

	resp = postLand(t, srv, changeID, "sekret")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200 land after approval, got %d: %s", resp.StatusCode, body)
	}
	var got landResponse
	json.NewDecoder(resp.Body).Decode(&got)
	tip, err := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if err != nil || tip != got.LandedSHA {
		t.Fatalf("expected trunk at %s, got %s (err %v)", got.LandedSHA, tip, err)
	}
}

// TestApproveNotARequiredOwner: approving an owner the tree doesn't require
// for these touched paths (billing-lib's owner - in the closure, not
// touched) is a structured client error, never silently recorded.
func TestApproveNotARequiredOwner(t *testing.T) {
	srv, _, changeID, _ := newApproveTestServer(t)
	defer srv.Close()

	resp := postApprove(t, srv, changeID, "sekret",
		`{"owner_ref": "group:billing-eng", "approved_by": "bob"}`)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
	}
	var ce clierr.Error
	if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ce.Code != "not_a_required_owner" {
		t.Fatalf("expected not_a_required_owner, got %+v", ce)
	}
	if !strings.Contains(ce.Suggestion, "group:commerce-eng") {
		t.Fatalf("expected the suggestion to name the actual required owners, got %q", ce.Suggestion)
	}
}

func TestApproveMissingFieldsIsStructuredError(t *testing.T) {
	srv, _, changeID, _ := newApproveTestServer(t)
	defer srv.Close()

	resp := postApprove(t, srv, changeID, "sekret", `{"owner_ref": "group:commerce-eng"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	var ce clierr.Error
	json.NewDecoder(resp.Body).Decode(&ce)
	if ce.Code != "missing_field" {
		t.Fatalf("expected missing_field, got %+v", ce)
	}
}

func TestApproveRequiresAuth(t *testing.T) {
	srv, _, changeID, _ := newApproveTestServer(t)
	defer srv.Close()

	resp := postApprove(t, srv, changeID, "",
		`{"owner_ref": "group:commerce-eng", "approved_by": "alice"}`)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}
}

func TestApproveNotFound(t *testing.T) {
	srv, _, _, _ := newApproveTestServer(t)
	defer srv.Close()

	resp := postApprove(t, srv, "no-such-change", "sekret",
		`{"owner_ref": "group:commerce-eng", "approved_by": "alice"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

// TestNoOwnersAnywhereMeansNoApprovalGate pins the anti-Boq default (§6.2,
// §7.3 "gaps visible; optionally blocking"): a project with no manifest
// owners, no OWNERS file, and no org default has NO owner requirements -
// the gate never invents an unsatisfiable requirement. This is exactly why
// wiring owners as "always outstanding" was rejected as a stopgap: it would
// have made every ownerless project permanently unlandable. (The fixture
// runs in the eval profile; outside it, a change with no owners AND no
// checks hits the separate default-deny posture - policy_gate_test.go -
// which blocks with a loud, actionable blocker, still never an
// unsatisfiable owner requirement.)
func TestNoOwnersAnywhereMeansNoApprovalGate(t *testing.T) {
	srv, _, changeID, _ := newLandTestServer(t) // its project declares no owners
	defer srv.Close()

	reqs := getMergeRequirements(t, srv, changeID)
	if len(reqs.RequiredOwners) != 0 {
		t.Fatalf("expected no owner requirements, got %v", reqs.RequiredOwners)
	}
	if !reqs.Mergeable {
		t.Fatalf("expected mergeable with no owners and no checks declared, got %+v", reqs)
	}
}

// TestAmendResetsOwnerApprovals is §13.5's approval-binding decision
// (2026-07-07, stage 12c) as a regression test for the exact bypass it
// closes: approve v1, amend to v2. The head change always invalidated
// check runs (keyed by (change, head_sha)) but the approval used to
// survive (keyed by change only), so once checks re-greened, v2 could
// land with a human gate satisfied against code no human ever saw.
func TestAmendResetsOwnerApprovals(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	const changeIDTrailer = "Change-Id: I0123456789abcdef0123456789abcdef01234567"
	repo.WriteFile("commerce/checkout/main.go", "package main // v1\n")
	repo.Commit("add main.go\n\n" + changeIDTrailer)
	_, head1 := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: head1, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}
	changeID := result.ChangeID

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Approve v1; the owner gate is satisfied.
	resp := postApprove(t, srv, changeID, "sekret", `{"owner_ref":"group:commerce-eng","approved_by":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("approve: %d: %s", resp.StatusCode, body)
	}
	reqs := getMergeRequirements(t, srv, changeID)
	if len(reqs.SatisfiedOwners) != 1 || len(reqs.OutstandingOwners) != 0 {
		t.Fatalf("expected the owner gate satisfied at v1, got satisfied=%v outstanding=%v", reqs.SatisfiedOwners, reqs.OutstandingOwners)
	}

	// Amend: a new head under the SAME Change-Id (what any re-push of the
	// magic ref produces).
	repo.WriteFile("commerce/checkout/main.go", "package main // v2 - never reviewed\n")
	repo.Commit("amend\n\n" + changeIDTrailer)
	_, head2 := pushCommit(t, repo, bare, "refs/for/main")
	result2 := processor.Process(context.Background(), RefUpdate{OldSHA: head1, NewSHA: head2, Ref: "refs/for/main"}, nil)
	if !result2.Accepted || result2.ChangeID != changeID {
		t.Fatalf("amend push not accepted as the same Change: %+v", result2)
	}

	// The stale approval must not count for v2.
	reqs = getMergeRequirements(t, srv, changeID)
	if len(reqs.OutstandingOwners) != 1 || len(reqs.SatisfiedOwners) != 0 {
		t.Fatalf("expected the amend to reset the owner gate, got satisfied=%v outstanding=%v", reqs.SatisfiedOwners, reqs.OutstandingOwners)
	}
	if reqs.Mergeable {
		t.Fatalf("expected not mergeable after amend, got %+v", reqs)
	}

	// Re-approving at v2 satisfies it again - reset, not permanent veto.
	resp = postApprove(t, srv, changeID, "sekret", `{"owner_ref":"group:commerce-eng","approved_by":"alice"}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("re-approve: %d: %s", resp.StatusCode, body)
	}
	reqs = getMergeRequirements(t, srv, changeID)
	if len(reqs.SatisfiedOwners) != 1 || len(reqs.OutstandingOwners) != 0 {
		t.Fatalf("expected the owner gate satisfied after re-approval at v2, got satisfied=%v outstanding=%v", reqs.SatisfiedOwners, reqs.OutstandingOwners)
	}
}

// newUploaderConsentServer seeds one open change pushed by `author` in a repo
// whose ROOT OWNERS file names `owner` (no manifest owners anywhere - the
// §6.10 genesis shape, resolved via §7.3 OWNERS inheritance). Deliberately
// NOT AllowUnpolicedLand: the owner requirement is what makes the policy
// resolve, so these tests also pin that an OWNERS-only org satisfies the
// default-deny posture.
func newUploaderConsentServer(t *testing.T, owner string, author Principal) (*httptest.Server, string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("OWNERS", "# seeded at org genesis (§6.10)\n"+owner+"\n")
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store, Principals: []Principal{author}}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, []string{"REMOTE_USER=" + author.Name})
	if !result.Accepted {
		t.Fatalf("seed push rejected: %+v", result)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", Principals: []Principal{author}}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return httptest.NewServer(handler), result.ChangeID
}

// TestAuthorAsOwnerSatisfiesOwnRequirement is the §6.10 uploader-consent
// rule (Gerrit's model): a HUMAN author who is themselves the required owner
// satisfies that requirement by having pushed the change - the push is the
// consent the gate collects. Without this, a path whose only owner is the
// author is permanently unlandable (self-approval is denied, actions.go),
// which would deadlock every fresh org the moment genesis seeds OWNERS with
// its creator. The land at the end is the solo-org bar end to end.
func TestAuthorAsOwnerSatisfiesOwnRequirement(t *testing.T) {
	srv, changeID := newUploaderConsentServer(t, "alice",
		Principal{Name: "alice", Token: "alice-token"})
	defer srv.Close()

	reqs := getMergeRequirements(t, srv, changeID)
	if len(reqs.RequiredOwners) != 1 || reqs.RequiredOwners[0] != "alice" {
		t.Fatalf("required owners: want [alice], got %v", reqs.RequiredOwners)
	}
	if len(reqs.SatisfiedOwners) != 1 || len(reqs.OutstandingOwners) != 0 {
		t.Fatalf("expected alice's own push to satisfy her requirement, got satisfied=%v outstanding=%v", reqs.SatisfiedOwners, reqs.OutstandingOwners)
	}
	if !reqs.Mergeable {
		t.Fatalf("expected the solo owner-author's change mergeable, got %+v", reqs)
	}

	resp := postLand(t, srv, changeID, "sekret")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected the solo-org land to succeed, got %d: %s", resp.StatusCode, body)
	}
}

// TestAgentAuthorshipNeverSelfSatisfies: §8.7's "no approving at all"
// includes an agent's own authorship - an OWNERS entry naming the agent
// stays outstanding on the agent's own change, so agent work always keeps
// a (non-author) approver in the loop.
func TestAgentAuthorshipNeverSelfSatisfies(t *testing.T) {
	srv, changeID := newUploaderConsentServer(t, "agent-fix",
		Principal{Name: "agent-fix", Token: "agent-token", IsAgent: true})
	defer srv.Close()

	reqs := getMergeRequirements(t, srv, changeID)
	if len(reqs.OutstandingOwners) != 1 || reqs.OutstandingOwners[0] != "agent-fix" {
		t.Fatalf("expected the agent's own requirement outstanding, got satisfied=%v outstanding=%v", reqs.SatisfiedOwners, reqs.OutstandingOwners)
	}
	if reqs.Mergeable {
		t.Fatalf("an agent-authored change must not become mergeable by its own authorship: %+v", reqs)
	}
}
