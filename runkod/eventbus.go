package runkod

// EventBus is §12.6's in-process live feed: the receive/land paths publish
// WorkspaceEvents and WatchWorkspace streams subscribe per workspace. One
// bus per org Server (Server.Events and Processor.Events point at the same
// instance), because the bus can only see its own process's receives - the
// stated single-replica assumption; an HA future needs a fan-out or leans
// on the stream's reconnect-refetch semantics.
//
// Delivery is lossy-with-coalescing: each subscription holds only the
// LATEST pending event plus a capacity-1 ready signal, so a slow or dead
// subscriber can never block a push and a burst collapses into one poke.
// That is sufficient by construction - frames are pokes, not data; the
// client refetches diff/timeline on every frame (§12.6). The guarantee is
// "at least one ready signal after the last publish", never a log.

import (
	"context"
	"log"
	"sync"
)

// EventBus fans workspace events out to per-workspace subscriptions.
// Nil-safe like ZoektIndexWorker: a nil *EventBus drops publishes and
// hands out already-done subscriptions.
type EventBus struct {
	mu     sync.Mutex
	closed bool
	subs   map[*BusSubscription]struct{}
}

// NewEventBus returns an empty bus.
func NewEventBus() *EventBus { return &EventBus{subs: make(map[*BusSubscription]struct{})} }

// BusSubscription is one WatchWorkspace stream's tap on the bus.
type BusSubscription struct {
	workspaceID string

	mu     sync.Mutex
	latest WorkspaceEvent
	has    bool

	ready chan struct{} // cap 1: "an event is pending"
	done  chan struct{} // closed on cancel or bus Close
}

// Ready signals that Take has something; consume the signal, then Take.
func (s *BusSubscription) Ready() <-chan struct{} { return s.ready }

// Done closes when the subscription is cancelled or the bus shuts down.
func (s *BusSubscription) Done() <-chan struct{} { return s.done }

// Take returns the pending event, newest wins; ok is false when a Ready
// signal raced an earlier Take that already drained it.
func (s *BusSubscription) Take() (WorkspaceEvent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has {
		return WorkspaceEvent{}, false
	}
	s.has = false
	return s.latest, true
}

// Subscribe registers a tap for workspaceID's events. The returned cancel
// is idempotent; after cancel (or bus Close) Done is closed and the
// subscription receives nothing further.
func (b *EventBus) Subscribe(workspaceID string) (*BusSubscription, func()) {
	sub := &BusSubscription{
		workspaceID: workspaceID,
		ready:       make(chan struct{}, 1),
		done:        make(chan struct{}),
	}
	if b == nil {
		close(sub.done)
		return sub, func() {}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(sub.done)
		return sub, func() {}
	}
	b.subs[sub] = struct{}{}
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		// Map presence guards the close: Close() and a racing double
		// cancel both funnel through b.mu, so done closes exactly once.
		if _, ok := b.subs[sub]; ok {
			delete(b.subs, sub)
			close(sub.done)
		}
	}
	return sub, cancel
}

// Publish delivers ev to every subscription watching its workspace.
// Never blocks: O(subs) under one mutex with non-blocking signals - safe
// to call from the receive funnel's hot path.
func (b *EventBus) Publish(ev WorkspaceEvent) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs {
		if sub.workspaceID != ev.WorkspaceID {
			continue
		}
		sub.mu.Lock()
		sub.latest = ev
		sub.has = true
		sub.mu.Unlock()
		select {
		case sub.ready <- struct{}{}:
		default: // a signal is already pending; the drain will Take the newest
		}
	}
}

// Close ends every subscription; further publishes drop and further
// subscribes come back already-done.
func (b *EventBus) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for sub := range b.subs {
		close(sub.done)
	}
	b.subs = nil
}

// recordWorkspaceEvent is the one write path for §12.6 activity: Store row
// first (the history), then the bus poke (the live signal). Insert failure
// logs and still publishes - a lost row costs one timeline entry, a lost
// poke only postpones the refetch to the next keepalive/reconnect; neither
// may fail the push or land that emitted it.
func recordWorkspaceEvent(ctx context.Context, store Store, bus *EventBus, ev WorkspaceEvent) {
	rec, err := store.RecordWorkspaceEvent(ctx, ev)
	if err != nil {
		log.Printf("runkod: record workspace event %s/%s: %v", ev.WorkspaceID, ev.Type, err)
		rec = ev
	}
	bus.Publish(rec)
}
