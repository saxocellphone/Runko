package runkod

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
)

// TestMemStoreLandedListingOrder pins finding #45: the landed listing
// reads in LANDING order (landed_at DESC, newest land first), not creation
// or key order - a change that lands later sorts above one that landed
// before it, and same-instant ties fall back to ChangeKey.
func TestMemStoreLandedListingOrder(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	clock := time.Date(2026, 7, 13, 20, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return clock }

	for _, key := range []string{"Ia", "Ib", "Ic"} {
		if _, err := s.CreateOrUpdateChange(ctx, key, "base", "head-"+key, "ref", "t", "", "", ""); err != nil {
			t.Fatalf("create %s: %v", key, err)
		}
	}
	// Ia lands first, Ib a minute later; Ic ties with Ib.
	if _, err := s.MarkChangeLanded(ctx, "Ia", "sha", "", false); err != nil {
		t.Fatalf("land Ia: %v", err)
	}
	clock = clock.Add(time.Minute)
	if _, err := s.MarkChangeLanded(ctx, "Ib", "sha", "", false); err != nil {
		t.Fatalf("land Ib: %v", err)
	}
	if _, err := s.MarkChangeLanded(ctx, "Ic", "sha", "", false); err != nil {
		t.Fatalf("land Ic: %v", err)
	}

	landed, err := s.ListChanges(ctx, "landed")
	if err != nil {
		t.Fatalf("ListChanges(landed): %v", err)
	}
	var keys []string
	for _, c := range landed {
		keys = append(keys, c.ChangeKey)
	}
	if len(keys) != 3 || keys[0] != "Ib" || keys[1] != "Ic" || keys[2] != "Ia" {
		t.Fatalf("landed listing: want landing order [Ib Ic Ia], got %v", keys)
	}
}

func TestMemStoreCreateOrUpdateChange(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()

	created, err := s.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title", "", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (create): %v", err)
	}
	if created.State != "open" {
		t.Fatalf("expected a new Change to start open, got %q", created.State)
	}

	updated, err := s.CreateOrUpdateChange(ctx, "Iabc", "base1", "head2", "refs/changes/1/head", "title", "", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (update): %v", err)
	}
	if updated.HeadSHA != "head2" {
		t.Fatalf("expected head_sha to advance, got %q", updated.HeadSHA)
	}

	got, ok, err := s.GetChange(ctx, "Iabc")
	if err != nil || !ok {
		t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}
	if got.HeadSHA != "head2" {
		t.Fatalf("expected GetChange to reflect the update, got %+v", got)
	}
}

func TestMemStoreUpsertCheckRunUpdatesInPlace(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()

	if err := s.UpsertCheckRun(ctx, "Iabc", "head1", checks.CheckRunView{Name: "unit", Status: checks.CheckStatusQueued}); err != nil {
		t.Fatalf("UpsertCheckRun: %v", err)
	}
	if err := s.UpsertCheckRun(ctx, "Iabc", "head1", checks.CheckRunView{Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess}); err != nil {
		t.Fatalf("UpsertCheckRun (update): %v", err)
	}

	runs, err := s.ListCheckRuns(ctx, "Iabc", "head1")
	if err != nil {
		t.Fatalf("ListCheckRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected exactly one run (updated in place), got %d: %+v", len(runs), runs)
	}
	if runs[0].Status != checks.CheckStatusCompleted || runs[0].Conclusion != checks.ConclusionSuccess {
		t.Fatalf("expected the run to reflect the latest status, got %+v", runs[0])
	}
}

func TestMemStoreListCheckRunsScopedToHeadSHA(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	s.UpsertCheckRun(ctx, "Iabc", "head1", checks.CheckRunView{Name: "unit", Status: checks.CheckStatusQueued})
	s.UpsertCheckRun(ctx, "Iabc", "head2", checks.CheckRunView{Name: "unit", Status: checks.CheckStatusQueued})

	runs, err := s.ListCheckRuns(ctx, "Iabc", "head1")
	if err != nil {
		t.Fatalf("ListCheckRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected runs from head2 to not leak into head1's list, got %+v", runs)
	}
}

func TestMemStoreWebhookOutboxLifecycle(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	now := time.Now()

	id, err := s.EnqueueWebhook(ctx, "change.opened", []byte(`{"type":"change.opened"}`))
	if err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}

	due, err := s.ListDueWebhookDeliveries(ctx, now)
	if err != nil || len(due) != 1 {
		t.Fatalf("expected 1 due delivery, got %d (err=%v)", len(due), err)
	}

	if err := s.RecordDeliveryResult(ctx, id, checks.DeliveryAttempt{Success: false, Err: errors.New("boom")}, time.Second, time.Minute, now); err != nil {
		t.Fatalf("RecordDeliveryResult (failure): %v", err)
	}
	due, _ = s.ListDueWebhookDeliveries(ctx, now)
	if len(due) != 0 {
		t.Fatalf("expected the failed delivery to not be due again until its backoff elapses, got %+v", due)
	}
	due, _ = s.ListDueWebhookDeliveries(ctx, now.Add(time.Hour))
	if len(due) != 1 {
		t.Fatalf("expected the failed delivery to become due again after its backoff, got %+v", due)
	}

	if err := s.RecordDeliveryResult(ctx, id, checks.DeliveryAttempt{Success: true}, time.Second, time.Minute, now); err != nil {
		t.Fatalf("RecordDeliveryResult (success): %v", err)
	}
	due, _ = s.ListDueWebhookDeliveries(ctx, now.Add(time.Hour))
	if len(due) != 0 {
		t.Fatalf("expected a delivered delivery to never be due again, got %+v", due)
	}
}

func TestMemStoreWebhookDeadLettersPastMaxAttempts(t *testing.T) {
	s := NewMemStore()
	ctx := context.Background()
	now := time.Now()
	id, _ := s.EnqueueWebhook(ctx, "test.event", []byte(`{}`))

	for i := 0; i < checks.MaxDeliveryAttempts; i++ {
		if err := s.RecordDeliveryResult(ctx, id, checks.DeliveryAttempt{Success: false, Err: errors.New("boom")}, time.Millisecond, time.Millisecond, now); err != nil {
			t.Fatalf("RecordDeliveryResult attempt %d: %v", i, err)
		}
	}
	due, _ := s.ListDueWebhookDeliveries(ctx, now.Add(time.Hour))
	if len(due) != 0 {
		t.Fatalf("expected a dead-lettered delivery to never be listed as due again, got %+v", due)
	}
}
