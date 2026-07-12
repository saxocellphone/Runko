package runkod

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

// agentTestServer: a full HTTP server (deploy token "sekret") over a bare
// repo with trunk, for driving mint/auth/funnel end to end.
func agentTestServer(t *testing.T) (*httptest.Server, *Server, *MemStore, *gitfixture.Repo, string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	srv := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store, Token: "sekret",
		SingleUseAgentWorkspaces: true,
	}
	handler, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	hs := httptest.NewServer(handler)
	t.Cleanup(hs.Close)
	return hs, srv, store, repo, bare
}

func mintAgent(t *testing.T, hs *httptest.Server, authHeader, task string) (name, token string, status int, body map[string]any) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"task": task})
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/agents", bytes.NewReader(payload))
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("Content-Type", "application/json")
	resp, err := hs.Client().Do(req)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	defer resp.Body.Close()
	body = map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	name, _ = body["name"].(string)
	tok, _ := body["token"].(string)
	return name, tok, resp.StatusCode, body
}

func whoami(t *testing.T, hs *httptest.Server, authHeader string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/whoami", nil)
	req.Header.Set("Authorization", authHeader)
	resp, err := hs.Client().Do(req)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// TestMintAgentPrincipalAndAuthenticate: the whole point - one API call
// mints a task-named identity whose token authenticates as an AGENT, both
// as a bearer token and as Basic name:token (the git transport's form).
func TestMintAgentPrincipalAndAuthenticate(t *testing.T) {
	hs, _, _, _, _ := agentTestServer(t)

	name, token, status, body := mintAgent(t, hs, "Bearer sekret", "fix-rail-alignment")
	if status != http.StatusCreated {
		t.Fatalf("mint: want 201, got %d: %v", status, body)
	}
	if !strings.HasPrefix(name, "agent-fix-rail-alignment-") {
		t.Fatalf("name must embed the task: %q", name)
	}
	if token == "" {
		t.Fatalf("mint must return the token exactly once")
	}

	// Bearer form.
	code, who := whoami(t, hs, "Bearer "+token)
	if code != http.StatusOK || who["name"] != name || who["is_agent"] != true {
		t.Fatalf("bearer whoami: want %s as agent, got %d %v", name, code, who)
	}
	// Basic name:token form (what git remotes carry).
	code, who = whoami(t, hs, basicAuth(name, token))
	if code != http.StatusOK || who["name"] != name || who["is_agent"] != true {
		t.Fatalf("basic whoami: want %s as agent, got %d %v", name, code, who)
	}
	// Wrong name with the right token must NOT authenticate.
	if code, _ := whoami(t, hs, basicAuth("someone-else", token)); code == http.StatusOK {
		t.Fatalf("name+token must BOTH match")
	}

	// Uniqueness: same task minted again gets a distinct suffix.
	name2, _, _, _ := mintAgent(t, hs, "Bearer sekret", "fix-rail-alignment")
	if name2 == name {
		t.Fatalf("two mints of one task must not collide: %q", name)
	}
}

// TestAgentsCannotMintAgents: no self-replication, no lifetime extension.
func TestAgentsCannotMintAgents(t *testing.T) {
	hs, _, _, _, _ := agentTestServer(t)
	_, token, _, _ := mintAgent(t, hs, "Bearer sekret", "some-task")

	_, _, status, body := mintAgent(t, hs, "Bearer "+token, "another-task")
	if status != http.StatusForbidden {
		t.Fatalf("want 403 for an agent minting, got %d: %v", status, body)
	}
	if body["Code"] != "agents_cannot_mint" {
		t.Fatalf("want agents_cannot_mint, got %v", body)
	}
}

// TestExpiredAndRevokedAgentTokensFailAuth: liveness is the credential.
func TestExpiredAndRevokedAgentTokensFailAuth(t *testing.T) {
	hs, _, store, _, _ := agentTestServer(t)
	ctx := context.Background()

	// Expired: inserted directly (mint clamps TTL to the future).
	expired := AgentPrincipal{
		Name: "agent-old-task-dead", Task: "old-task",
		TokenHash: hashAgentToken("expired-token"), ExpiresAt: time.Now().Add(-time.Hour),
	}
	if _, err := store.MintAgentPrincipal(ctx, expired); err != nil {
		t.Fatalf("seed expired: %v", err)
	}
	if code, _ := whoami(t, hs, "Bearer expired-token"); code == http.StatusOK {
		t.Fatalf("an expired agent token must not authenticate")
	}

	// Revoked: minted live, then killed.
	name, token, _, _ := mintAgent(t, hs, "Bearer sekret", "kill-me")
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/agents/"+name+"/revoke", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	if resp, err := hs.Client().Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %v %v", err, resp)
	}
	if code, _ := whoami(t, hs, "Bearer "+token); code == http.StatusOK {
		t.Fatalf("a revoked agent token must not authenticate")
	}
}

// TestMintedAgentIsPolicedAtReceive: the payoff - a minted identity arms
// §8.7 at the funnel with zero further config. The default policy requires
// workspace affinity, so the agent's push WITHOUT a workspace origin is
// refused; from its OWN workspace it lands in policy and passes.
func TestMintedAgentIsPolicedAtReceive(t *testing.T) {
	hs, srv, store, repo, bare := agentTestServer(t)
	ctx := context.Background()

	name, _, _, _ := mintAgent(t, hs, "Bearer sekret", "policed-task")
	p := newTestProcessor(bare, store)

	repo.WriteFile("svc/feature.txt", "v1\n")
	repo.Commit("feature\n\nChange-Id: I0123456789012345678901234567890123456789")
	oldSHA, tip := pushCommit(t, repo, bare, "refs/for/main")

	// No workspace origin: the default agent policy refuses.
	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: tip, Ref: "refs/for/main"}, []string{"REMOTE_USER=" + name})
	if result.Accepted {
		t.Fatalf("a minted agent without workspace affinity must be refused (§8.7 default policy)")
	}

	// From its own workspace: within policy, accepted.
	if _, err := store.CreateWorkspace(ctx, Workspace{
		ID: "policed-task-ws", Owner: name, SnapshotRef: "refs/workspaces/policed-task-ws/head",
		Status: "active", WriteAllowlist: []string{"svc"},
	}); err != nil {
		t.Fatalf("workspace: %v", err)
	}
	result = p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: tip, Ref: "refs/for/main"}, []string{
		"REMOTE_USER=" + name,
		"GIT_PUSH_OPTION_COUNT=1",
		"GIT_PUSH_OPTION_0=workspace=policed-task-ws",
	})
	if !result.Accepted {
		t.Fatalf("in-policy push from the agent's own workspace must pass: %+v", result)
	}

	// And the single-use policy recognizes the minted owner: concluding
	// the last change closes the workspace (store-backed agent, not flag).
	if _, apiErr := srv.abandonChangeCore(ctx, result.ChangeID, nil); apiErr != nil {
		t.Fatalf("abandon: %+v", apiErr)
	}
	ws, _, _ := store.GetWorkspace(ctx, "policed-task-ws")
	if ws.Status != "closed" {
		t.Fatalf("a minted agent's concluded workspace must auto-close, got %q", ws.Status)
	}
}

// TestMintValidation: slug rules and the TTL cap.
func TestMintValidation(t *testing.T) {
	hs, _, _, _, _ := agentTestServer(t)

	for _, bad := range []string{"", "UPPER", "has space", "slash/task", strings.Repeat("x", 41)} {
		if _, _, status, _ := mintAgent(t, hs, "Bearer sekret", bad); status != http.StatusBadRequest {
			t.Fatalf("task %q must be a 400, got %d", bad, status)
		}
	}

	payload, _ := json.Marshal(map[string]any{"task": "long-task", "ttl_seconds": int((30 * 24 * time.Hour).Seconds())})
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/agents", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer sekret")
	resp, err := hs.Client().Do(req)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.ExpiresAt.After(time.Now().Add(agentMaxTTL + time.Hour)) {
		t.Fatalf("TTL must clamp to the %s cap, got expiry %s", agentMaxTTL, out.ExpiresAt)
	}
}

func basicAuth(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}
