package runkod

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/index"
)

// TestScanCacheFollowsTrunk pins indexedProjectsAt's key contract: entries
// are keyed by the RESOLVED trunk-tip SHA, so a repeated read serves from
// cache while a trunk move (new SHA) is visible on the very next read - a
// ref-name-keyed cache would keep serving yesterday's tree here.
func TestScanCacheFollowsTrunk(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml", "schema: project/v1\nname: checkout-api\ntype: service\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: NewMemStore(), Processor: newTestProcessor(bare, NewMemStore()), Token: "sekret"}
	handler, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	httpSrv := httptest.NewServer(handler)
	defer httpSrv.Close()

	listNames := func() []string {
		t.Helper()
		resp := authedGet(t, httpSrv, "/api/projects", "sekret")
		if resp.StatusCode != 200 {
			t.Fatalf("GET /api/projects: %d: %s", resp.StatusCode, readBody(t, resp))
		}
		var indexed []index.IndexedProject
		if err := json.Unmarshal([]byte(readBody(t, resp)), &indexed); err != nil {
			t.Fatalf("parse projects: %v", err)
		}
		names := make([]string, len(indexed))
		for i, p := range indexed {
			names[i] = p.Name
		}
		return names
	}

	// Twice: the first read scans and fills the cache, the second serves
	// from it - both must agree.
	for i := 0; i < 2; i++ {
		if names := listNames(); len(names) != 1 || names[0] != "checkout-api" {
			t.Fatalf("read %d: want [checkout-api], got %v", i, names)
		}
	}

	// Advance trunk with a second project; the next read resolves a new
	// tip SHA and must see it immediately.
	repo.WriteFile("billing/PROJECT.yaml", "schema: project/v1\nname: billing\ntype: service\n")
	repo.Commit("add billing")
	pushCommit(t, repo, bare, "refs/heads/main")

	names := listNames()
	if len(names) != 2 {
		t.Fatalf("after trunk move: want 2 projects, got %v", names)
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
	}
	if !seen["checkout-api"] || !seen["billing"] {
		t.Fatalf("after trunk move: want checkout-api and billing, got %v", names)
	}
}
