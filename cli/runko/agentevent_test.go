package main

// runko agent event / hooks tests (§12.6.1): the fake-server pattern
// (approve_test.go) for the wire contract, a real temp git repo for the
// runko.workspace discovery rule, stdin piping for --from-hook, and
// t.Setenv for the verb-local env credential fallback.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
)

// newActivityWorkspaceDir builds a throwaway git repo bound to a
// workspace id the way `workspace create/attach` leaves real worktrees.
func newActivityWorkspaceDir(t *testing.T, id string) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet")
	mustGit(t, dir, "config", "runko.workspace", id)
	return dir
}

// activityFakeServer records the one POST the verb must make.
func activityFakeServer(t *testing.T, wantPath, wantAuth string, gotBody *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != wantPath {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != wantAuth {
			t.Fatalf("expected %q, got %q", wantAuth, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		json.NewEncoder(w).Encode(map[string]int{"recorded": 1})
	}))
}

func TestAgentEventPostsToWorkspaceActivity(t *testing.T) {
	dir := newActivityWorkspaceDir(t, "obs")
	var got map[string]any
	srv := activityFakeServer(t, "/api/workspaces/obs/activity", "Bearer sekret", &got)
	defer srv.Close()

	err := execCLI("agent", "event",
		"--kind", "read", "--detail", "runkod/api.go", "--session", "sess-1",
		"--dir", dir, "--runkod-url", srv.URL, "--token", "sekret", "--json",
	)
	if err != nil {
		t.Fatalf("cmdAgentEvent: %v", err)
	}
	events, ok := got["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("expected one event in the batch body, got %v", got)
	}
	ev := events[0].(map[string]any)
	if ev["kind"] != "read" || ev["detail"] != "runkod/api.go" || ev["session_id"] != "sess-1" {
		t.Fatalf("event body wrong: %v", ev)
	}
}

func TestAgentEventFromHookMapsToolsToKinds(t *testing.T) {
	cases := []struct {
		in         string
		kind, want string
	}{
		{`{"session_id":"s","tool_name":"Read","tool_input":{"file_path":"a.go"}}`, "read", "a.go"},
		{`{"tool_name":"Edit","tool_input":{"file_path":"b.ts"}}`, "edit", "b.ts"},
		{`{"tool_name":"Write","tool_input":{"file_path":"c.md"}}`, "edit", "c.md"},
		{`{"tool_name":"NotebookEdit","tool_input":{"notebook_path":"d.ipynb"}}`, "edit", "d.ipynb"},
		{`{"tool_name":"Bash","tool_input":{"command":"go test ./..."}}`, "command", "go test ./..."},
		{`{"tool_name":"Grep","tool_input":{"pattern":"func main"}}`, "search", "func main"},
		{`{"tool_name":"SomeNewTool","tool_input":{}}`, "note", "SomeNewTool"},
	}
	for _, c := range cases {
		var in hookInput
		if err := json.Unmarshal([]byte(c.in), &in); err != nil {
			t.Fatalf("unmarshal %s: %v", c.in, err)
		}
		kind, detail := eventFromHook(in)
		if kind != c.kind || detail != c.want {
			t.Fatalf("eventFromHook(%s) = (%q, %q), want (%q, %q)", c.in, kind, detail, c.kind, c.want)
		}
	}
}

func TestAgentEventFromHookEndToEnd(t *testing.T) {
	dir := newActivityWorkspaceDir(t, "obs")
	var got map[string]any
	srv := activityFakeServer(t, "/api/workspaces/obs/activity", "Bearer sekret", &got)
	defer srv.Close()

	// Pipe the hook JSON in as the harness would.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldStdin }()
	go func() {
		w.WriteString(`{"session_id":"sess-9","tool_name":"Bash","tool_input":{"command":"make check"}}`)
		w.Close()
	}()

	err = execCLI("agent", "event", "--from-hook", "--dir", dir, "--runkod-url", srv.URL, "--token", "sekret", "--json")
	if err != nil {
		t.Fatalf("cmdAgentEvent --from-hook: %v", err)
	}
	ev := got["events"].([]any)[0].(map[string]any)
	if ev["kind"] != "command" || ev["detail"] != "make check" || ev["session_id"] != "sess-9" {
		t.Fatalf("hook-derived event wrong: %v", ev)
	}
}

func TestAgentEventEnvCredentialFallback(t *testing.T) {
	dir := newActivityWorkspaceDir(t, "obs")
	var got map[string]any
	srv := activityFakeServer(t, "/api/workspaces/obs/activity", "Bearer env-token", &got)
	defer srv.Close()

	t.Setenv("RUNKO_TOKEN", "env-token")
	t.Setenv("RUNKO_RUNKOD_URL", srv.URL)
	if err := execCLI("agent", "event", "--kind", "note", "--detail", "hello", "--dir", dir, "--json"); err != nil {
		t.Fatalf("cmdAgentEvent with env credential: %v", err)
	}
	if len(got["events"].([]any)) != 1 {
		t.Fatalf("expected the env-authed POST to land, got %v", got)
	}

	// Token without URL is the structured missing_url, not a guess.
	t.Setenv("RUNKO_RUNKOD_URL", "")
	err := execCLI("agent", "event", "--kind", "note", "--detail", "hello", "--dir", dir)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "missing_url" {
		t.Fatalf("want missing_url, got %v", err)
	}
}

func TestAgentEventOutsideWorkspaceIsStructured(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet")
	err := execCLI("agent", "event", "--kind", "read", "--detail", "x", "--dir", dir, "--runkod-url", "http://127.0.0.1:0", "--token", "t")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_a_workspace" {
		t.Fatalf("want not_a_workspace, got %v", err)
	}
}

func TestAgentHooksSnippetIsValidJSON(t *testing.T) {
	if !json.Valid([]byte(agentHooksSnippet)) {
		t.Fatalf("hooks snippet must be valid JSON:\n%s", agentHooksSnippet)
	}
	var snippet struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(agentHooksSnippet), &snippet); err != nil {
		t.Fatalf("unmarshal snippet: %v", err)
	}
	post := snippet.Hooks["PostToolUse"]
	if len(post) != 1 || len(post[0].Hooks) != 1 {
		t.Fatalf("want one PostToolUse matcher with one hook, got %+v", snippet.Hooks)
	}
	if cmd := post[0].Hooks[0].Command; cmd != "runko agent event --from-hook || true" {
		t.Fatalf("hook command must be best-effort --from-hook, got %q", cmd)
	}
}
