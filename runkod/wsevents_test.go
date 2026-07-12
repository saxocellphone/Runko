package runkod

// Workspace-event store tests (§12.6, stage 18): the stats-only timeline
// behind the live workspace view. IDs must be strictly increasing (the
// timeline orders and clients dedupe by them), history is capped, and
// deletion is whole-workspace.

import (
	"context"
	"fmt"
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
