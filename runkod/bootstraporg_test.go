package runkod

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/receive"
)

// newOwnerlessServer seeds the pre-genesis org shape (§6.10 retrofit
// target): a born trunk holding a real project but NO owners anywhere -
// under default-deny nothing can land, including the OWNERS fix itself.
func newOwnerlessServer(t *testing.T, seed func(*gitfixture.Repo)) (*httptest.Server, string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	seed(repo)
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret",
		Principals: []Principal{
			{Name: "val", Token: "val-token"},
			{Name: "robo", Token: "robo-token", IsAgent: true},
		}}
	handler, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, bare
}

func postBootstrap(t *testing.T, srv *httptest.Server, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/org/bootstrap", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	return resp
}

// TestOrgBootstrapOpensSelfLandableChange pins the verb's whole point: one
// command turns an ownerless org into a governed one via a Change the
// caller can land immediately - owners resolve from the change's own head
// tree, and the human owner-author self-satisfies by uploader consent.
func TestOrgBootstrapOpensSelfLandableChange(t *testing.T) {
	srv, bare := newOwnerlessServer(t, func(repo *gitfixture.Repo) {
		repo.WriteFile("hello/PROJECT.yaml", "schema: project/v1\nname: hello\ntype: app\n")
		repo.WriteFile("hello/main.go", "package main\n")
	})

	resp := postBootstrap(t, srv, "val-token")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap: expected 200, got %d", resp.StatusCode)
	}
	var out struct {
		SeededGenesis bool   `json:"seeded_genesis"`
		ChangeID      string `json:"change_id"`
		Title         string `json:"title"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.SeededGenesis || out.ChangeID == "" || !strings.Contains(out.Title, "val") {
		t.Fatalf("outcome = %+v", out)
	}

	// The change's head tree carries the governance minimum: root OWNERS
	// naming the caller, and the root manifest (this trunk had none).
	g := gitstore.New(bare)
	headRef := core.Revision("refs/changes/" + out.ChangeID + "/head")
	owners, err := g.GetBlob(headRef, "OWNERS")
	if err != nil {
		t.Fatalf("read OWNERS at change head: %v", err)
	}
	if !strings.Contains(string(owners.Content), "val") {
		t.Fatalf("OWNERS does not name the caller:\n%s", owners.Content)
	}
	if _, err := g.GetBlob(headRef, "PROJECT.yaml"); err != nil {
		t.Fatalf("root manifest missing from change head: %v", err)
	}

	// Self-landable NOW: required owners resolve (val, from the head
	// tree) and are satisfied by authorship, so default-deny does not
	// fire and nothing else blocks.
	mrResp := authedGet(t, srv, "/api/changes/"+out.ChangeID+"/merge-requirements", "sekret")
	var mr struct {
		Mergeable bool
		Owners    struct {
			Required  []string
			Satisfied []string
		}
		Blockers []string
	}
	if err := json.NewDecoder(mrResp.Body).Decode(&mr); err != nil {
		t.Fatalf("decode merge-requirements: %v", err)
	}
	if !mr.Mergeable {
		t.Fatalf("bootstrap change not mergeable: %+v", mr)
	}
}

// TestOrgBootstrapRefusesGovernedTrunk: once owners resolve anywhere, the
// verb steps aside - ownership evolves through ordinary changes.
func TestOrgBootstrapRefusesGovernedTrunk(t *testing.T) {
	srv, _ := newOwnerlessServer(t, func(repo *gitfixture.Repo) {
		repo.WriteFile("OWNERS", "someone\n")
		repo.WriteFile("hello/PROJECT.yaml", "schema: project/v1\nname: hello\ntype: app\n")
	})
	resp := postBootstrap(t, srv, "val-token")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409, got %d", resp.StatusCode)
	}
	var ce struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil || ce.Code != "already_governed" {
		t.Fatalf("code = %q (err %v)", ce.Code, err)
	}
}

// TestOrgBootstrapRefusesAgentsAndAnonymous: naming who governs is a human
// product action (§8.7), and the anonymous deploy token has no name to
// record as the first owner.
func TestOrgBootstrapRefusesAgentsAndAnonymous(t *testing.T) {
	srv, _ := newOwnerlessServer(t, func(repo *gitfixture.Repo) {
		repo.WriteFile("hello/PROJECT.yaml", "schema: project/v1\nname: hello\ntype: app\n")
	})
	for token, wantCode := range map[string]string{
		"robo-token": "agents_cannot_bootstrap_org",
		"sekret":     "bootstrap_needs_identity",
	} {
		resp := postBootstrap(t, srv, token)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("token %s: expected 403, got %d", token, resp.StatusCode)
		}
		var ce struct {
			Code string `json:"code"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil || ce.Code != wantCode {
			t.Fatalf("token %s: code = %q (err %v)", token, ce.Code, err)
		}
	}
}

// TestOrgBootstrapUnbornTrunkSeedsGenesis: nothing ever landed, so there is
// no history to review against - the full genesis is written directly, the
// same standing as org creation (§6.10).
func TestOrgBootstrapUnbornTrunkSeedsGenesis(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Token: "sekret",
		Principals: []Principal{{Name: "val", Token: "val-token"}}}
	handler, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)

	resp := postBootstrap(t, ts, "val-token")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var out struct {
		SeededGenesis bool `json:"seeded_genesis"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || !out.SeededGenesis {
		t.Fatalf("seeded_genesis = %v (err %v)", out.SeededGenesis, err)
	}

	g := gitstore.New(bare)
	trunk, err := g.ResolveRef("refs/heads/main")
	if err != nil {
		t.Fatalf("trunk still unborn after bootstrap: %v", err)
	}
	owners, err := g.GetBlob(trunk, "OWNERS")
	if err != nil || !strings.Contains(string(owners.Content), "val") {
		t.Fatalf("genesis OWNERS = %q (err %v)", owners.Content, err)
	}
	if _, err := g.GetBlob(trunk, "AGENTS.md"); err != nil {
		t.Fatalf("genesis AGENTS.md missing: %v", err)
	}
}

// TestOrgBootstrapAdminGate: where org roles exist (store-backed account
// under a directory), only org admins may decide who governs.
func TestOrgBootstrapAdminGate(t *testing.T) {
	srv, _ := func() (*httptest.Server, string) {
		bare := newBareRepo(t)
		repo := gitfixture.New(t)
		repo.WriteFile("hello/PROJECT.yaml", "schema: project/v1\nname: hello\ntype: app\n")
		repo.Commit("initial")
		pushCommit(t, repo, bare, "refs/heads/main")

		store := NewMemStore()
		if err := store.EnsureOrg(context.Background(), "acme"); err != nil {
			t.Fatalf("EnsureOrg: %v", err)
		}
		if err := store.UpsertOrgMember(context.Background(), "acme", "mallory", "member"); err != nil {
			t.Fatalf("UpsertOrgMember: %v", err)
		}
		srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Token: "sekret",
			OrgName: "acme", Directory: store,
			Principals: []Principal{{Name: "mallory", Token: "mallory-token", Stored: true}}}
		handler, err := srv.Handler()
		if err != nil {
			t.Fatalf("Handler: %v", err)
		}
		ts := httptest.NewServer(handler)
		t.Cleanup(ts.Close)
		return ts, bare
	}()

	resp := postBootstrap(t, srv, "mallory-token")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin member, got %d", resp.StatusCode)
	}
	var ce struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil || ce.Code != "not_org_admin" {
		t.Fatalf("code = %q (err %v)", ce.Code, err)
	}
}
