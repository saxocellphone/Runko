package runkod

// The 2026-07-24 enforcement split's server half (platform/README.md
// Decisions): an agent push with ackable policy findings is accepted, owes
// the reserved agent-policy check, and only a human acknowledgement - the
// "extra button" - completes it. These tests drive the real HTTP surface
// over a real bare repo: mint at receive, gate at requirements, ack (and
// its refusals), the reserved-name guard on the report API.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/receive"
)

const ackChangeID = "Iaaaa5555bbbb6666cccc7777dddd8888eeee9999"

// newAckFixture seeds one agent-authored change that touches a denylisted
// path (security/**) - accepted since the split, carrying the minted
// agent-policy check - and serves the full API over it.
func newAckFixture(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("security/policy.yaml", "rules: []\n")
	repo.Commit("touch security\n\nChange-Id: " + ackChangeID)
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	relaxed := receive.DefaultAgentPolicy()
	relaxed.RequireWorkspaceAffinity = false
	principals := []Principal{
		{Name: "alice", Token: "alice-tok"},
		{Name: "bumpbot", Token: "bot-tok", IsAgent: true, Policy: relaxed},
	}
	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store, Principals: principals}
	result := processor.Process(context.Background(),
		RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"},
		[]string{"REMOTE_USER=bumpbot"})
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret", Principals: principals}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, result.ChangeID
}

func postAckPolicy(t *testing.T, srv *httptest.Server, changeID, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+changeID+"/ack-policy", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST ack-policy: %v", err)
	}
	return resp
}

// TestAgentPolicyCheckGatesUntilAcked walks the whole intended loop: the
// minted check joins the required set and fails the gate; a human ack
// completes it green; a second ack is a conflict.
func TestAgentPolicyCheckGatesUntilAcked(t *testing.T) {
	srv, changeID := newAckFixture(t)

	reqs := getMergeRequirements(t, srv, changeID)
	requiredHas := false
	for _, n := range reqs.RequiredChecks {
		if n == checks.AgentPolicyCheckName {
			requiredHas = true
		}
	}
	if !requiredHas {
		t.Fatalf("agent-policy must join the required set, got %v", reqs.RequiredChecks)
	}
	failingHas := false
	for _, n := range reqs.FailingChecks {
		if n == checks.AgentPolicyCheckName {
			failingHas = true
		}
	}
	if !failingHas || reqs.Mergeable {
		t.Fatalf("unacked agent-policy must fail the gate, got failing=%v mergeable=%v", reqs.FailingChecks, reqs.Mergeable)
	}

	resp := postAckPolicy(t, srv, changeID, "alice-tok", `{}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("ack as a human principal: expected 200, got %d: %s", resp.StatusCode, body)
	}
	resp.Body.Close()

	reqs = getMergeRequirements(t, srv, changeID)
	passingHas := false
	for _, n := range reqs.PassingChecks {
		if n == checks.AgentPolicyCheckName {
			passingHas = true
		}
	}
	if !passingHas {
		t.Fatalf("acked agent-policy must pass, got passing=%v failing=%v", reqs.PassingChecks, reqs.FailingChecks)
	}

	resp = postAckPolicy(t, srv, changeID, "alice-tok", `{}`)
	if resp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("double ack: expected 409, got %d: %s", resp.StatusCode, body)
	}
	if ce := decodeClierr(t, resp); ce.Code != "already_acknowledged" {
		t.Fatalf("expected already_acknowledged, got %+v", ce)
	}
}

// TestAgentCannotAckPolicy: an agent must never complete its own (or any)
// policy leash - the exact reason the check name is reserved.
func TestAgentCannotAckPolicy(t *testing.T) {
	srv, changeID := newAckFixture(t)
	resp := postAckPolicy(t, srv, changeID, "bot-tok", `{}`)
	if resp.StatusCode != http.StatusForbidden {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403, got %d: %s", resp.StatusCode, body)
	}
	if ce := decodeClierr(t, resp); ce.Code != "agent_ack_denied" {
		t.Fatalf("expected agent_ack_denied, got %+v", ce)
	}
}

// TestAckPolicyWithNothingToAck: a change with no minted agent-policy run
// has nothing to acknowledge.
func TestAckPolicyWithNothingToAck(t *testing.T) {
	srv, changeID, _ := newPrincipalTestServer(t, testPrincipals) // human-pushed, clean
	resp := postAckPolicy(t, srv, changeID, "alice-tok", `{}`)
	if resp.StatusCode != http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, body)
	}
	if ce := decodeClierr(t, resp); ce.Code != "nothing_to_acknowledge" {
		t.Fatalf("expected nothing_to_acknowledge, got %+v", ce)
	}
}

// TestReportCheckRefusesReservedName: no external reporter - CI, an agent
// holding a token, anyone - may write the reserved check.
func TestReportCheckRefusesReservedName(t *testing.T) {
	srv, changeID := newAckFixture(t)
	body := `{"name":"agent-policy","external_id":"x","reporter":"github-actions","status":"completed","conclusion":"success"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+changeID+"/checks", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer sekret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST check: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 403 for the reserved name, got %d: %s", resp.StatusCode, b)
	}
	if ce := decodeClierr(t, resp); ce.Code != "reserved_check_name" {
		t.Fatalf("expected reserved_check_name, got %+v", ce)
	}
}
