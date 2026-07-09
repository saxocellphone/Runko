package runkod

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

// §15.2 public_read: the anonymous-read auth matrix over the real hub
// (mem mode). The git transport half lives in cmd/runkod's e2e suite -
// hideRefs and upload-pack-only need a real git client.

func anonPost(t *testing.T, srv *httptest.Server, path string, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestPublicReadAuthMatrix(t *testing.T) {
	srv, _ := newTestHub(t, true)
	hubSignup(t, srv, "alice", "alicepw123")
	if status, body := hubDo(t, srv, "POST", "/api/orgs", "alice", "alicepw123", "", map[string]string{"name": "acme"}); status != http.StatusCreated {
		t.Fatalf("create org: %d %v", status, body)
	}

	// Private by default: anonymous reads 401 on every surface.
	if status, _ := hubDo(t, srv, "GET", "/o/acme/api/projects", "", "", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("private org must 401 anonymous REST reads, got %d", status)
	}
	if status := anonPost(t, srv, "/o/acme/runko.v1.ProjectService/ListProjects", "{}"); status != http.StatusUnauthorized {
		t.Fatalf("private org must 401 anonymous RPC reads, got %d", status)
	}

	// The org admin flips public_read on.
	if status, body := hubDo(t, srv, "PUT", "/api/orgs/acme/settings", "alice", "alicepw123", "", map[string]any{"public_read": true}); status != http.StatusOK {
		t.Fatalf("enable public_read: %d %v", status, body)
	}

	// Allowlisted reads open up...
	if status, _ := hubDo(t, srv, "GET", "/o/acme/api/projects", "", "", "", nil); status != http.StatusOK {
		t.Fatalf("public org must serve anonymous GET /api/projects, got %d", status)
	}
	if status, _ := hubDo(t, srv, "GET", "/o/acme/api/changes", "", "", "", nil); status != http.StatusOK {
		t.Fatalf("public org must serve anonymous GET /api/changes, got %d", status)
	}
	if status := anonPost(t, srv, "/o/acme/runko.v1.ProjectService/ListProjects", "{}"); status != http.StatusOK {
		t.Fatalf("public org must serve anonymous ListProjects RPC, got %d", status)
	}

	// ...and nothing else does. Writes:
	if status, _ := hubDo(t, srv, "POST", "/o/acme/api/workspaces", "", "", "", map[string]any{"name": "w", "owner": "x", "projects": []string{"p"}}); status != http.StatusUnauthorized {
		t.Fatalf("anonymous workspace create must 401, got %d", status)
	}
	// Non-allowlisted reads (workspaces expose owners + write allowlists):
	if status, _ := hubDo(t, srv, "GET", "/o/acme/api/workspaces", "", "", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous workspace list must 401, got %d", status)
	}
	if status := anonPost(t, srv, "/o/acme/runko.v1.WorkspaceService/ListWorkspaces", "{}"); status != http.StatusUnauthorized {
		t.Fatalf("anonymous ListWorkspaces RPC must 401, got %d", status)
	}
	// Settings/members stay gated:
	if status, _ := hubDo(t, srv, "GET", "/api/orgs/acme/settings", "", "", "", nil); status == http.StatusOK {
		t.Fatalf("anonymous settings read must not be public")
	}

	// Presented-but-wrong credentials never downgrade to anonymous.
	if status, _ := hubDo(t, srv, "GET", "/o/acme/api/projects", "", "", "wrong-token", nil); status != http.StatusUnauthorized {
		t.Fatalf("a bad token must 401 even on a public org, got %d", status)
	}

	// Other orgs are untouched: the default org stays private.
	if status, _ := hubDo(t, srv, "GET", "/api/changes", "", "", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("public_read on acme must not open the default org, got %d", status)
	}
}

// TestPublicReadRefusedWithRestrictedProjects pins the §15.2 fail-closed
// guard: restricted-read must hold at every surface or not at all, and
// anonymous fetch has no per-principal filtering until §12.3 Phase B.
func TestPublicReadRefusedWithRestrictedProjects(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("secret-svc/PROJECT.yaml", "schema: project/v1\nname: secret-svc\ntype: service\nvisibility: restricted\n")
	repo.Commit("seed")

	restricted, err := restrictedProjects(repo.Dir)
	if err != nil {
		t.Fatalf("restrictedProjects: %v", err)
	}
	if len(restricted) != 1 || restricted[0] != "secret-svc" {
		t.Fatalf("expected [secret-svc], got %v", restricted)
	}
}

// Enabling public_read on an org whose trunk is unborn must not error -
// there are no manifests yet, so nothing can be restricted.
func TestRestrictedProjectsUnbornTrunk(t *testing.T) {
	restricted, err := restrictedProjects(newBareRepo(t))
	if err != nil {
		t.Fatalf("unborn trunk must not error: %v", err)
	}
	if restricted != nil {
		t.Fatalf("expected nil, got %v", restricted)
	}
}
