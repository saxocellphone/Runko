package runkod

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/internal/dbtest"
)

// pgTestDSN resets the schema (via dbtest.Connect, same as every other
// *_pg_test.go in this repo) and returns the raw DSN string
// BootstrapPostgresStore needs - it opens its own pool, separate from
// dbtest's, exercising the real production bootstrap path rather than a
// test-only shortcut.
func pgTestDSN(t *testing.T) string {
	t.Helper()
	dbtest.Connect(t) // resets schema; skips the test if RUNKO_TEST_DATABASE_URL is unset
	return os.Getenv("RUNKO_TEST_DATABASE_URL")
}

func newTestPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := pgTestDSN(t)
	store, err := BootstrapPostgresStore(context.Background(), dsn, t.Name(), "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore: %v", err)
	}
	t.Cleanup(store.Pool.Close)
	return store
}

func TestBootstrapPostgresStoreIsIdempotent(t *testing.T) {
	dsn := pgTestDSN(t)
	ctx := context.Background()

	first, err := BootstrapPostgresStore(ctx, dsn, t.Name(), "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore (first): %v", err)
	}
	defer first.Pool.Close()

	second, err := BootstrapPostgresStore(ctx, dsn, t.Name(), "main")
	if err != nil {
		t.Fatalf("BootstrapPostgresStore (second): %v", err)
	}
	defer second.Pool.Close()

	if first.OrgID != second.OrgID || first.MonorepoID != second.MonorepoID || first.AuthorActorID != second.AuthorActorID {
		t.Fatalf("expected bootstrapping twice with the same org name to return the same IDs, got %+v vs %+v", first, second)
	}
}

func TestPostgresStoreCreateOrUpdateChangeAndGetChange(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	created, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (create): %v", err)
	}
	if created.State != "open" {
		t.Fatalf("expected a new Change to start open, got %+v", created)
	}

	updated, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head2", "refs/changes/1/head", "title")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (update): %v", err)
	}
	if updated.HeadSHA != "head2" {
		t.Fatalf("expected head_sha to advance, got %+v", updated)
	}

	got, ok, err := store.GetChange(ctx, "Iabc")
	if err != nil || !ok {
		t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}
	if got.HeadSHA != "head2" {
		t.Fatalf("expected GetChange to reflect the update, got %+v", got)
	}

	if _, ok, err := store.GetChange(ctx, "no-such-change"); err != nil || ok {
		t.Fatalf("expected GetChange for an unknown key to return ok=false, got ok=%v err=%v", ok, err)
	}
}

func TestPostgresStoreCheckRunUpsertReflectsLatestStatus(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title"); err != nil {
		t.Fatalf("CreateOrUpdateChange: %v", err)
	}

	if err := store.UpsertCheckRun(ctx, "Iabc", "head1", checks.CheckRunView{Name: "unit", Status: checks.CheckStatusQueued}); err != nil {
		t.Fatalf("UpsertCheckRun (queued): %v", err)
	}
	if err := store.UpsertCheckRun(ctx, "Iabc", "head1", checks.CheckRunView{Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess}); err != nil {
		t.Fatalf("UpsertCheckRun (completed): %v", err)
	}

	runs, err := store.ListCheckRuns(ctx, "Iabc", "head1")
	if err != nil {
		t.Fatalf("ListCheckRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected the status transition to update the SAME run (attempt 1), not create a second one, got %d: %+v", len(runs), runs)
	}
	if runs[0].Status != checks.CheckStatusCompleted || runs[0].Conclusion != checks.ConclusionSuccess {
		t.Fatalf("expected the run to reflect the latest status, got %+v", runs[0])
	}
}

func TestPostgresStoreWebhookOutboxLifecycle(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()
	now := time.Now()

	id, err := store.EnqueueWebhook(ctx, "change.opened", []byte(`{"type":"change.opened"}`))
	if err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}

	due, err := store.ListDueWebhookDeliveries(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries: %v", err)
	}
	if len(due) != 1 || due[0].ID != id {
		t.Fatalf("expected the enqueued delivery to be due, got %+v", due)
	}

	if err := store.RecordDeliveryResult(ctx, id, checks.DeliveryAttempt{Success: false, Err: context.DeadlineExceeded}, time.Hour, time.Hour, now); err != nil {
		t.Fatalf("RecordDeliveryResult (failure): %v", err)
	}
	due, err = store.ListDueWebhookDeliveries(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries after failure: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected the failed delivery to not be due again until its backoff elapses, got %+v", due)
	}

	if err := store.RecordDeliveryResult(ctx, id, checks.DeliveryAttempt{Success: true}, time.Hour, time.Hour, now); err != nil {
		t.Fatalf("RecordDeliveryResult (success): %v", err)
	}
	due, err = store.ListDueWebhookDeliveries(ctx, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries after success: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected a delivered delivery to never be due again, got %+v", due)
	}
}
