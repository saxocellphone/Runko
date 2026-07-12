package runkod

// Agent-activity ingest tests (§12.6.1, stage 19): the client-claimed
// feed. Store semantics mirror the §12.6 timeline (increasing IDs, cap,
// whole-workspace delete) plus the batch/latest verbs; the HTTP surface
// pins the snapshot-push ownership rules, the soft kind vocabulary, the
// content policy (truncation + redaction, fail-closed on scanner error),
// and the one-poke-per-batch bus contract.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/platform/receive"
)

func TestMemStoreWorkspaceActivityRoundTrip(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()

	batch, err := s.RecordWorkspaceActivity(ctx, []WorkspaceActivity{
		{WorkspaceID: "ws-a", Actor: "agent-x", Kind: WorkspaceActivityRead, Detail: "runkod/api.go", SessionID: "sess-1"},
		{WorkspaceID: "ws-a", Actor: "agent-x", Kind: WorkspaceActivityCommand, Detail: "go test ./..."},
	})
	if err != nil {
		t.Fatalf("RecordWorkspaceActivity: %v", err)
	}
	if len(batch) != 2 || batch[0].ID == 0 || batch[1].ID <= batch[0].ID || batch[0].OccurredAt.IsZero() {
		t.Fatalf("expected two rows with increasing IDs and timestamps, got %+v", batch)
	}
	if _, err := s.RecordWorkspaceActivity(ctx, []WorkspaceActivity{
		{WorkspaceID: "ws-b", Kind: WorkspaceActivityEdit, Detail: "web/src/App.tsx"},
	}); err != nil {
		t.Fatalf("RecordWorkspaceActivity (ws-b): %v", err)
	}

	evs, err := s.ListWorkspaceActivity(ctx, "ws-a", 0, 0)
	if err != nil {
		t.Fatalf("ListWorkspaceActivity: %v", err)
	}
	if len(evs) != 2 || evs[0].ID != batch[1].ID || evs[1].ID != batch[0].ID {
		t.Fatalf("expected newest-first order, got %+v", evs)
	}
	if evs[1].SessionID != "sess-1" || evs[1].Detail != "runkod/api.go" {
		t.Fatalf("fields lost in round-trip: %+v", evs[1])
	}
	if evs, _ := s.ListWorkspaceActivity(ctx, "ws-a", 1, 1); len(evs) != 1 || evs[0].ID != batch[0].ID {
		t.Fatalf("limit/offset should slice newest-first, got %+v", evs)
	}

	latest, err := s.LatestWorkspaceActivity(ctx, []string{"ws-a", "ws-b", "ws-none"})
	if err != nil {
		t.Fatalf("LatestWorkspaceActivity: %v", err)
	}
	if latest["ws-a"].ID != batch[1].ID || latest["ws-b"].Kind != WorkspaceActivityEdit {
		t.Fatalf("latest rows wrong: %+v", latest)
	}
	if _, ok := latest["ws-none"]; ok {
		t.Fatalf("a workspace that never reported must be absent, got %+v", latest)
	}

	if err := s.DeleteWorkspaceActivity(ctx, "ws-a"); err != nil {
		t.Fatalf("DeleteWorkspaceActivity: %v", err)
	}
	if evs, _ := s.ListWorkspaceActivity(ctx, "ws-a", 0, 0); len(evs) != 0 {
		t.Fatalf("expected empty feed after delete, got %+v", evs)
	}
	if evs, _ := s.ListWorkspaceActivity(ctx, "ws-b", 0, 0); len(evs) != 1 {
		t.Fatalf("ws-b's feed should survive ws-a's delete, got %+v", evs)
	}
}

func TestMemStoreWorkspaceActivityPruneToCap(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()

	for i := 0; i < workspaceActivityRetentionCap+5; i++ {
		if _, err := s.RecordWorkspaceActivity(ctx, []WorkspaceActivity{
			{WorkspaceID: "ws-a", Kind: WorkspaceActivityRead, Detail: fmt.Sprintf("file%d", i)},
		}); err != nil {
			t.Fatalf("RecordWorkspaceActivity #%d: %v", i, err)
		}
	}
	evs, err := s.ListWorkspaceActivity(ctx, "ws-a", 0, 0)
	if err != nil {
		t.Fatalf("ListWorkspaceActivity: %v", err)
	}
	if len(evs) != workspaceActivityRetentionCap {
		t.Fatalf("expected feed capped at %d, got %d", workspaceActivityRetentionCap, len(evs))
	}
	if evs[0].Detail != fmt.Sprintf("file%d", workspaceActivityRetentionCap+4) {
		t.Fatalf("newest row missing after prune: %+v", evs[0])
	}
	if evs[len(evs)-1].Detail != "file5" {
		t.Fatalf("expected the 5 oldest rows pruned, oldest survivor is %+v", evs[len(evs)-1])
	}
}

// activityTestServer builds an httptest server around a MemStore seeded
// with one agent-owned workspace, the wsevents_test harness shape.
func activityTestServer(t *testing.T) (*httptest.Server, *MemStore, *EventBus) {
	t.Helper()
	bare := newBareRepo(t)
	store := NewMemStore()
	if _, err := store.CreateWorkspace(context.Background(), Workspace{
		ID: "checkout-fixes", Owner: "agent-a", SnapshotRef: "refs/workspaces/checkout-fixes/head", Status: "active",
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	bus := NewEventBus()
	server := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store,
		Processor: newTestProcessor(bare, store), Token: "sekret", Events: bus,
		Principals: []Principal{
			{Name: "agent-a", Token: "agent-a-token", IsAgent: true, Policy: receive.DefaultAgentPolicy()},
			{Name: "mallory", Token: "mallory-token"},
			{Name: "op", Token: "op-token", Admin: true},
		},
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, store, bus
}

func postActivity(t *testing.T, srv *httptest.Server, workspaceID, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/workspaces/"+workspaceID+"/activity", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST activity: %v", err)
	}
	return resp
}

func TestRecordWorkspaceActivityHTTP(t *testing.T) {
	srv, store, bus := activityTestServer(t)
	ctx := context.Background()
	body := `{"events":[{"kind":"read","detail":"runkod/api.go","session_id":"sess-1"},{"kind":"SomeNewTool","detail":"whatever"}]}`

	// Auth and ownership gates, the §12.2 snapshot-push rules.
	if resp := postActivity(t, srv, "checkout-fixes", "wrong", body); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: want 401, got %d", resp.StatusCode)
	}
	if resp := postActivity(t, srv, "nope", "agent-a-token", body); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown workspace: want 404, got %d", resp.StatusCode)
	}
	resp := postActivity(t, srv, "checkout-fixes", "mallory-token", body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-owner: want 403, got %d", resp.StatusCode)
	}
	var cerr struct {
		Code string `json:"code"`
	}
	json.NewDecoder(resp.Body).Decode(&cerr)
	if cerr.Code != "not_workspace_owner" {
		t.Fatalf("non-owner: want not_workspace_owner, got %q", cerr.Code)
	}

	// The owner reports; a subscriber gets exactly one agent_activity poke.
	sub, cancel := bus.Subscribe("checkout-fixes")
	defer cancel()
	resp = postActivity(t, srv, "checkout-fixes", "agent-a-token", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner: want 200, got %d", resp.StatusCode)
	}
	var out map[string]int
	json.NewDecoder(resp.Body).Decode(&out)
	if out["recorded"] != 2 {
		t.Fatalf("want recorded=2, got %v", out)
	}
	select {
	case <-sub.Ready():
	default:
		t.Fatalf("an accepted batch must poke the workspace's live feed")
	}
	if ev, ok := sub.Take(); !ok || ev.Type != WorkspaceEventAgentActivity || ev.Actor != "agent-a" {
		t.Fatalf("poke should be agent_activity by the reporting actor, got %+v ok=%v", ev, ok)
	}

	// Stored rows: actor from the principal, unknown kind coerced to note.
	evs, _ := store.ListWorkspaceActivity(ctx, "checkout-fixes", 0, 0)
	if len(evs) != 2 || evs[1].Kind != WorkspaceActivityRead || evs[1].Actor != "agent-a" || evs[1].SessionID != "sess-1" {
		t.Fatalf("stored rows wrong: %+v", evs)
	}
	if evs[0].Kind != WorkspaceActivityNote {
		t.Fatalf("unknown kind must coerce to note, got %q", evs[0].Kind)
	}

	// Operators and the anonymous deploy token pass the owner gate.
	if resp := postActivity(t, srv, "checkout-fixes", "op-token", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator: want 200, got %d", resp.StatusCode)
	}
	if resp := postActivity(t, srv, "checkout-fixes", "sekret", body); resp.StatusCode != http.StatusOK {
		t.Fatalf("deploy token: want 200, got %d", resp.StatusCode)
	}

	// Content policy: detail truncates to the rune cap.
	long := strings.Repeat("x", workspaceActivityDetailMax+50)
	resp = postActivity(t, srv, "checkout-fixes", "agent-a-token",
		`{"events":[{"kind":"command","detail":"`+long+`"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("long detail: want 200, got %d", resp.StatusCode)
	}
	evs, _ = store.ListWorkspaceActivity(ctx, "checkout-fixes", 1, 0)
	if got := len([]rune(evs[0].Detail)); got != workspaceActivityDetailMax {
		t.Fatalf("detail should truncate to %d runes, got %d", workspaceActivityDetailMax, got)
	}

	// Batch caps: empty and oversized both refuse with structured errors.
	if resp := postActivity(t, srv, "checkout-fixes", "agent-a-token", `{"events":[]}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty batch: want 400, got %d", resp.StatusCode)
	}
	var big strings.Builder
	big.WriteString(`{"events":[`)
	for i := 0; i <= workspaceActivityBatchMax; i++ {
		if i > 0 {
			big.WriteString(",")
		}
		big.WriteString(`{"kind":"read","detail":"f"}`)
	}
	big.WriteString(`]}`)
	resp = postActivity(t, srv, "checkout-fixes", "agent-a-token", big.String())
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized batch: want 400, got %d", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&cerr)
	if cerr.Code != "batch_too_large" {
		t.Fatalf("oversized batch: want batch_too_large, got %q", cerr.Code)
	}

	// A closed workspace refuses reports, the snapshot-push rule.
	if _, err := store.CreateWorkspace(ctx, Workspace{
		ID: "done-task", Owner: "agent-a", SnapshotRef: "refs/workspaces/done-task/head", Status: "closed",
	}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	resp = postActivity(t, srv, "done-task", "agent-a-token", body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("closed workspace: want 409, got %d", resp.StatusCode)
	}
	json.NewDecoder(resp.Body).Decode(&cerr)
	if cerr.Code != "workspace_closed" {
		t.Fatalf("closed workspace: want workspace_closed, got %q", cerr.Code)
	}
}

// stubScanner scripts receive.SecretScanner for redaction tests - the
// scripted-fake-binary pattern without the binary (the interface is the
// contract; gitleaks_test.go covers the real subprocess).
type stubScanner struct {
	findings []receive.SecretFinding
	err      error
}

func (s stubScanner) Scan(files []receive.FileContent) ([]receive.SecretFinding, error) {
	return s.findings, s.err
}

func TestActivitySecretRedaction(t *testing.T) {
	mk := func(scanner receive.SecretScanner) *Server {
		store := NewMemStore()
		if _, err := store.CreateWorkspace(context.Background(), Workspace{
			ID: "ws", Owner: "", SnapshotRef: "refs/workspaces/ws/head", Status: "active",
		}); err != nil {
			t.Fatalf("CreateWorkspace: %v", err)
		}
		return &Server{Store: store, Processor: &Processor{Scanner: scanner}}
	}
	events := []activityEventBody{
		{Kind: "command", Detail: "go test ./..."},
		{Kind: "command", Detail: "curl -H 'Authorization: Bearer hunter2'"},
	}

	// A finding redacts exactly the flagged event.
	s := mk(stubScanner{findings: []receive.SecretFinding{{Path: "1", RuleID: "generic-api-key"}}})
	if _, apiErr := s.recordWorkspaceActivityCore(context.Background(), "ws", nil, events); apiErr != nil {
		t.Fatalf("core: %+v", apiErr)
	}
	evs, _ := s.Store.ListWorkspaceActivity(context.Background(), "ws", 0, 0)
	if evs[0].Detail != "[redacted: generic-api-key]" {
		t.Fatalf("flagged event must be redacted, got %q", evs[0].Detail)
	}
	if evs[1].Detail != "go test ./..." {
		t.Fatalf("clean sibling must survive intact, got %q", evs[1].Detail)
	}

	// A scanner error redacts the whole batch - fail-closed (§12.6.1).
	s = mk(stubScanner{err: errors.New("gitleaks exploded")})
	if _, apiErr := s.recordWorkspaceActivityCore(context.Background(), "ws", nil, events); apiErr != nil {
		t.Fatalf("core: %+v", apiErr)
	}
	evs, _ = s.Store.ListWorkspaceActivity(context.Background(), "ws", 0, 0)
	for _, ev := range evs {
		if ev.Detail != "[redacted: scan_error]" {
			t.Fatalf("scanner error must redact every event, got %q", ev.Detail)
		}
	}

	// No Processor wired (eval profile) means no scan and no panic.
	s = mk(nil)
	s.Processor = nil
	if _, apiErr := s.recordWorkspaceActivityCore(context.Background(), "ws", nil, events[:1]); apiErr != nil {
		t.Fatalf("core without processor: %+v", apiErr)
	}
}
