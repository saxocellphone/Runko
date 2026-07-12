package runkod

// Workspace-event store tests (§12.6, stage 18): the stats-only timeline
// behind the live workspace view. IDs must be strictly increasing (the
// timeline orders and clients dedupe by them), history is capped, and
// deletion is whole-workspace.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMemStoreWorkspaceEventsRoundTrip(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()

	first, err := s.RecordWorkspaceEvent(ctx, WorkspaceEvent{
		Type: WorkspaceEventSnapshotPushed, WorkspaceID: "ws-a", Branch: "head",
		Actor: "agent-x", SHA: "aaa111", FilesChanged: 2, Additions: 10, Deletions: 3,
	})
	if err != nil {
		t.Fatalf("RecordWorkspaceEvent: %v", err)
	}
	if first.ID == 0 || first.OccurredAt.IsZero() {
		t.Fatalf("expected assigned ID and timestamp, got %+v", first)
	}
	second, err := s.RecordWorkspaceEvent(ctx, WorkspaceEvent{
		Type: WorkspaceEventChangePushed, WorkspaceID: "ws-a", Branch: "head",
		Actor: "agent-x", SHA: "bbb222", ChangeKey: "Iabc",
	})
	if err != nil {
		t.Fatalf("RecordWorkspaceEvent: %v", err)
	}
	if second.ID <= first.ID {
		t.Fatalf("IDs must be strictly increasing: first=%d second=%d", first.ID, second.ID)
	}
	// Another workspace's event must not leak into ws-a's timeline.
	if _, err := s.RecordWorkspaceEvent(ctx, WorkspaceEvent{
		Type: WorkspaceEventSnapshotPushed, WorkspaceID: "ws-b", SHA: "ccc333",
	}); err != nil {
		t.Fatalf("RecordWorkspaceEvent: %v", err)
	}

	evs, err := s.ListWorkspaceEvents(ctx, "ws-a", 0, 0)
	if err != nil {
		t.Fatalf("ListWorkspaceEvents: %v", err)
	}
	if len(evs) != 2 || evs[0].ID != second.ID || evs[1].ID != first.ID {
		t.Fatalf("expected newest-first [second, first], got %+v", evs)
	}
	if evs[1].FilesChanged != 2 || evs[1].Additions != 10 || evs[1].Deletions != 3 {
		t.Fatalf("numstat totals lost: %+v", evs[1])
	}

	// Paging: limit/offset slice the newest-first order.
	if evs, _ := s.ListWorkspaceEvents(ctx, "ws-a", 1, 0); len(evs) != 1 || evs[0].ID != second.ID {
		t.Fatalf("limit=1 should return newest only, got %+v", evs)
	}
	if evs, _ := s.ListWorkspaceEvents(ctx, "ws-a", 1, 1); len(evs) != 1 || evs[0].ID != first.ID {
		t.Fatalf("offset=1 should return the older event, got %+v", evs)
	}
	if evs, _ := s.ListWorkspaceEvents(ctx, "ws-a", 0, 99); len(evs) != 0 {
		t.Fatalf("offset past the end should return empty, got %+v", evs)
	}

	if err := s.DeleteWorkspaceEvents(ctx, "ws-a"); err != nil {
		t.Fatalf("DeleteWorkspaceEvents: %v", err)
	}
	if evs, _ := s.ListWorkspaceEvents(ctx, "ws-a", 0, 0); len(evs) != 0 {
		t.Fatalf("expected empty timeline after delete, got %+v", evs)
	}
	if evs, _ := s.ListWorkspaceEvents(ctx, "ws-b", 0, 0); len(evs) != 1 {
		t.Fatalf("ws-b's timeline should survive ws-a's delete, got %+v", evs)
	}
}

func TestMemStoreWorkspaceEventsPruneToCap(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()

	for i := 0; i < workspaceEventRetentionCap+7; i++ {
		if _, err := s.RecordWorkspaceEvent(ctx, WorkspaceEvent{
			Type: WorkspaceEventSnapshotPushed, WorkspaceID: "ws-a",
			SHA: fmt.Sprintf("sha%d", i),
		}); err != nil {
			t.Fatalf("RecordWorkspaceEvent #%d: %v", i, err)
		}
	}
	evs, err := s.ListWorkspaceEvents(ctx, "ws-a", 0, 0)
	if err != nil {
		t.Fatalf("ListWorkspaceEvents: %v", err)
	}
	if len(evs) != workspaceEventRetentionCap {
		t.Fatalf("expected timeline capped at %d, got %d", workspaceEventRetentionCap, len(evs))
	}
	// Oldest-first pruning: the newest event survives, the first ones go.
	if evs[0].SHA != fmt.Sprintf("sha%d", workspaceEventRetentionCap+6) {
		t.Fatalf("newest event missing after prune: %+v", evs[0])
	}
	if evs[len(evs)-1].SHA != "sha7" {
		t.Fatalf("expected the 7 oldest events pruned, oldest survivor is %+v", evs[len(evs)-1])
	}
}

// TestChangeLifecycleRecordsWorkspaceEvents walks push -> land -> push ->
// abandon through the real funnel and the HTTP verbs, asserting the §12.6
// timeline rows arrive in order with change keys, actors, and pokes.
func TestChangeLifecycleRecordsWorkspaceEvents(t *testing.T) {
	p, store, repo, bare := originFixture(t)
	ctx := context.Background()
	bus := NewEventBus()
	p.Events = bus

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add a feature\n\nChange-Id: I0123456789012345678901234567890123456789")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/for/main")
	result := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, []string{
		"GIT_PUSH_OPTION_COUNT=1",
		"GIT_PUSH_OPTION_0=workspace=checkout-fixes",
	})
	if !result.Accepted {
		t.Fatalf("push rejected: %+v", result)
	}

	evs, _ := store.ListWorkspaceEvents(ctx, "checkout-fixes", 0, 0)
	if len(evs) != 1 || evs[0].Type != WorkspaceEventChangePushed || evs[0].ChangeKey != result.ChangeID {
		t.Fatalf("expected one change_pushed event for the tip, got %+v", evs)
	}
	if evs[0].Branch != "head" || evs[0].SHA != headSHA || evs[0].FilesChanged == 0 {
		t.Fatalf("change_pushed should carry the default branch, tip sha, and file count: %+v", evs[0])
	}

	// Land over the wire (§13.5's verb), same bus on the Server side.
	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: p, Token: "sekret", AllowUnpolicedLand: true, Events: bus}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	sub, cancel := bus.Subscribe("checkout-fixes")
	defer cancel()

	if resp := postLand(t, srv, result.ChangeID, "sekret"); resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("land: %d: %s", resp.StatusCode, body)
	}
	evs, _ = store.ListWorkspaceEvents(ctx, "checkout-fixes", 0, 0)
	if len(evs) != 2 || evs[0].Type != WorkspaceEventChangeLanded || evs[0].ChangeKey != result.ChangeID || evs[0].SHA == "" {
		t.Fatalf("expected change_landed newest with the landed sha, got %+v", evs)
	}
	select {
	case <-sub.Ready():
	default:
		t.Fatalf("a land must poke the workspace's live feed")
	}
	sub.Take()

	// A second change through the same origin, then the abandon verb.
	repo.WriteFile("other.txt", "v1\n")
	repo.Commit("another\n\nChange-Id: I9876543210987654321098765432109876543210")
	prevSHA := headSHA
	_, head2 := pushCommit(t, repo, bare, "refs/for/main")
	result2 := p.Process(ctx, RefUpdate{OldSHA: prevSHA, NewSHA: head2, Ref: "refs/for/main"}, []string{
		"GIT_PUSH_OPTION_COUNT=1",
		"GIT_PUSH_OPTION_0=workspace=checkout-fixes",
	})
	if !result2.Accepted {
		t.Fatalf("second push rejected: %+v", result2)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+result2.ChangeID+"/abandon", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	resp, err := srv.Client().Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("abandon: %v status=%v", err, resp.Status)
	}
	evs, _ = store.ListWorkspaceEvents(ctx, "checkout-fixes", 0, 0)
	if len(evs) != 4 || evs[0].Type != WorkspaceEventChangeAbandoned || evs[0].ChangeKey != result2.ChangeID {
		t.Fatalf("expected [abandoned, pushed, landed, pushed], got %+v", evs)
	}
	select {
	case <-sub.Ready():
	default:
		t.Fatalf("an abandon must poke the workspace's live feed")
	}
}
