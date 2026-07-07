package runkod

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingIndexer is a fake search.Indexer that counts Index calls -
// standing in for a real ZoektIndexer so these tests exercise the debounce
// logic itself, not a subprocess.
type countingIndexer struct {
	count atomic.Int64
	done  chan struct{}
	mu    sync.Mutex
}

func (c *countingIndexer) Index(_ context.Context, _ string) error {
	c.count.Add(1)
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	if done != nil {
		select {
		case done <- struct{}{}:
		default:
		}
	}
	return nil
}

func TestZoektIndexWorkerCoalescesBurstsIntoOneRun(t *testing.T) {
	indexer := &countingIndexer{done: make(chan struct{}, 10)}
	worker := &ZoektIndexWorker{Indexer: indexer, RepoDir: "/repo", Debounce: 30 * time.Millisecond}

	for i := 0; i < 5; i++ {
		worker.Trigger()
	}

	select {
	case <-indexer.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("expected the debounced run to fire")
	}
	// Give any (incorrect) extra runs a moment to have shown up too.
	time.Sleep(100 * time.Millisecond)

	if got := indexer.count.Load(); got != 1 {
		t.Fatalf("expected exactly 1 Index call for 5 rapid Triggers, got %d", got)
	}
}

func TestZoektIndexWorkerRunsAgainAfterPreviousCompletes(t *testing.T) {
	indexer := &countingIndexer{done: make(chan struct{}, 10)}
	worker := &ZoektIndexWorker{Indexer: indexer, RepoDir: "/repo", Debounce: 10 * time.Millisecond}

	worker.Trigger()
	<-indexer.done
	time.Sleep(20 * time.Millisecond)

	worker.Trigger()
	<-indexer.done

	if got := indexer.count.Load(); got != 2 {
		t.Fatalf("expected 2 separate Index calls for 2 non-overlapping Triggers, got %d", got)
	}
}

func TestZoektIndexWorkerNilIsNoOp(t *testing.T) {
	var worker *ZoektIndexWorker
	worker.Trigger() // must not panic
}

func TestZoektIndexWorkerNilIndexerIsNoOp(t *testing.T) {
	worker := &ZoektIndexWorker{}
	worker.Trigger()
	time.Sleep(10 * time.Millisecond)
	if err := worker.LastErr(); err != nil {
		t.Fatalf("expected no run (and no error) with a nil Indexer, got %v", err)
	}
}

func TestZoektIndexWorkerRecordsError(t *testing.T) {
	worker := &ZoektIndexWorker{Indexer: erroringIndexer{}, RepoDir: "/repo", Debounce: time.Millisecond}
	worker.Trigger()
	deadline := time.After(time.Second)
	for worker.LastErr() == nil {
		select {
		case <-deadline:
			t.Fatalf("expected LastErr to be set")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

type erroringIndexer struct{}

func (erroringIndexer) Index(_ context.Context, _ string) error {
	return context.DeadlineExceeded
}
