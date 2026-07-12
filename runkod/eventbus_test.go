package runkod

// EventBus tests (§12.6): the live feed's contract is small but precise -
// publish never blocks, bursts coalesce to the newest event, and there is
// at least one ready signal after the last publish. The runkod-race lane
// exercises the concurrent paths.

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestEventBusPublishAndTake(t *testing.T) {
	b := NewEventBus()
	sub, cancel := b.Subscribe("ws-a")
	defer cancel()

	b.Publish(WorkspaceEvent{ID: 1, Type: WorkspaceEventSnapshotPushed, WorkspaceID: "ws-a"})

	select {
	case <-sub.Ready():
	case <-time.After(time.Second):
		t.Fatalf("expected a ready signal after publish")
	}
	ev, ok := sub.Take()
	if !ok || ev.ID != 1 || ev.Type != WorkspaceEventSnapshotPushed {
		t.Fatalf("Take = %+v ok=%v", ev, ok)
	}
	if _, ok := sub.Take(); ok {
		t.Fatalf("a second Take must report nothing pending")
	}
}

func TestEventBusCoalescesToLatest(t *testing.T) {
	b := NewEventBus()
	sub, cancel := b.Subscribe("ws-a")
	defer cancel()

	for i := int64(1); i <= 50; i++ {
		b.Publish(WorkspaceEvent{ID: i, WorkspaceID: "ws-a"})
	}
	<-sub.Ready()
	ev, ok := sub.Take()
	if !ok || ev.ID != 50 {
		t.Fatalf("expected the burst to coalesce to the newest event (50), got %+v ok=%v", ev, ok)
	}
	// The cap-1 ready channel may hold one more stale signal from the
	// burst - it must drain to an empty Take, not a duplicate.
	select {
	case <-sub.Ready():
		if ev, ok := sub.Take(); ok {
			t.Fatalf("stale ready signal must find nothing pending, got %+v", ev)
		}
	default:
	}
}

func TestEventBusScopedToWorkspace(t *testing.T) {
	b := NewEventBus()
	a, cancelA := b.Subscribe("ws-a")
	defer cancelA()
	other, cancelB := b.Subscribe("ws-b")
	defer cancelB()

	b.Publish(WorkspaceEvent{ID: 7, WorkspaceID: "ws-b"})

	select {
	case <-a.Ready():
		t.Fatalf("ws-a's subscription must not see ws-b's event")
	default:
	}
	<-other.Ready()
	if ev, ok := other.Take(); !ok || ev.ID != 7 {
		t.Fatalf("ws-b subscription: %+v ok=%v", ev, ok)
	}
}

// TestEventBusPublishNeverBlocks pins the §12.6 slow-client policy: a
// subscriber that never drains cannot stall a publisher - Publish returns
// and later subscribers still get poked.
func TestEventBusPublishNeverBlocks(t *testing.T) {
	b := NewEventBus()
	_, cancelDead := b.Subscribe("ws-a") // never drained
	defer cancelDead()

	done := make(chan struct{})
	go func() {
		for i := int64(0); i < 1000; i++ {
			b.Publish(WorkspaceEvent{ID: i, WorkspaceID: "ws-a"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Publish blocked on an undrained subscriber")
	}
}

func TestEventBusCancelAndCloseAreIdempotent(t *testing.T) {
	b := NewEventBus()
	sub, cancel := b.Subscribe("ws-a")
	cancel()
	cancel() // double cancel must not panic (close-once is map-guarded)
	select {
	case <-sub.Done():
	default:
		t.Fatalf("Done must be closed after cancel")
	}
	b.Publish(WorkspaceEvent{ID: 1, WorkspaceID: "ws-a"}) // into the void, fine

	still, cancelStill := b.Subscribe("ws-a")
	defer cancelStill()
	b.Close()
	b.Close() // idempotent
	select {
	case <-still.Done():
	default:
		t.Fatalf("Close must end open subscriptions")
	}
	late, _ := b.Subscribe("ws-a")
	select {
	case <-late.Done():
	default:
		t.Fatalf("a post-Close subscribe must come back already done")
	}
}

func TestEventBusNilSafe(t *testing.T) {
	var b *EventBus
	b.Publish(WorkspaceEvent{WorkspaceID: "ws-a"}) // no-op
	sub, cancel := b.Subscribe("ws-a")
	cancel()
	select {
	case <-sub.Done():
	default:
		t.Fatalf("nil-bus subscription must be born done")
	}
	b.Close()

	// recordWorkspaceEvent with a nil bus still writes the history row.
	s := NewMemStore()
	recordWorkspaceEvent(context.Background(), s, nil, WorkspaceEvent{
		Type: WorkspaceEventSnapshotPushed, WorkspaceID: "ws-a",
	})
	if evs, _ := s.ListWorkspaceEvents(context.Background(), "ws-a", 0, 0); len(evs) != 1 {
		t.Fatalf("expected the row without the bus, got %+v", evs)
	}
}

// TestEventBusConcurrentUse is the -race lane's fodder: publishers,
// subscribers, and cancels interleaving must be data-race free.
func TestEventBusConcurrentUse(t *testing.T) {
	b := NewEventBus()
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := int64(0); i < 200; i++ {
				b.Publish(WorkspaceEvent{ID: i, WorkspaceID: "ws-a"})
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				sub, cancel := b.Subscribe("ws-a")
				select {
				case <-sub.Ready():
					sub.Take()
				default:
				}
				cancel()
			}
		}()
	}
	wg.Wait()
	b.Close()
}
