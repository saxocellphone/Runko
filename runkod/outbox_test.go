package runkod

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/saxocellphone/runko/checks"
)

func TestOutboxWorkerDeliversToRealServer(t *testing.T) {
	var gotSignature string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSignature = r.Header.Get(checks.SignatureHeader)
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		gotBody = buf
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := NewMemStore()
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, "change.opened", []byte(`{"type":"change.opened"}`)); err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}

	worker := &OutboxWorker{Store: store, URL: server.URL, Secret: []byte("shh")}
	n, err := worker.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 delivery attempted, got %d", n)
	}
	if gotSignature == "" {
		t.Fatalf("expected a signature header on the delivered request")
	}
	if string(gotBody) != `{"type":"change.opened"}` {
		t.Fatalf("expected the payload to be delivered verbatim, got %q", gotBody)
	}

	due, _ := store.ListDueWebhookDeliveries(ctx, time.Now().Add(time.Hour))
	if len(due) != 0 {
		t.Fatalf("expected the delivered webhook to never be due again, got %+v", due)
	}
}

func TestOutboxWorkerRecordsFailureAndBacksOff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	store := NewMemStore()
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, "test.event", []byte(`{}`)); err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}

	worker := &OutboxWorker{Store: store, URL: server.URL, Secret: []byte("shh"), BackoffMin: time.Hour, BackoffMax: time.Hour}
	if _, err := worker.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	due, _ := store.ListDueWebhookDeliveries(ctx, time.Now())
	if len(due) != 0 {
		t.Fatalf("expected the failed delivery to not be immediately due again (backoff), got %+v", due)
	}
	due, _ = store.ListDueWebhookDeliveries(ctx, time.Now().Add(2*time.Hour))
	if len(due) != 1 {
		t.Fatalf("expected the failed delivery to become due again after its backoff, got %+v", due)
	}
}

func TestOutboxWorkerRunStopsOnContextCancel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store := NewMemStore()
	if _, err := store.EnqueueWebhook(context.Background(), "test.event", []byte(`{}`)); err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}

	worker := &OutboxWorker{Store: store, URL: server.URL, Secret: []byte("shh")}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx, 10*time.Millisecond)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("expected Run to stop shortly after context cancellation")
	}

	due, _ := store.ListDueWebhookDeliveries(context.Background(), time.Now())
	if len(due) != 0 {
		t.Fatalf("expected the delivery to have been delivered by the background loop, got %+v", due)
	}
}
