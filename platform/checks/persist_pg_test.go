package checks

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/dbgen"
	"github.com/saxocellphone/runko/internal/dbtest"
)

var errBoom = errors.New("boom")

// seedChange creates a minimal Change row, the FK parent every check_runs
// row needs, against a live Postgres.
func seedChange(t *testing.T, ctx context.Context, db dbgen.DBTX, q *dbgen.Queries, fx dbtest.Fixture) *dbgen.Change {
	t.Helper()
	change, err := q.CreateChange(ctx, db, dbgen.CreateChangeParams{
		MonorepoID:        fx.MonorepoID,
		ChangeKey:         "Iabc123",
		State:             dbgen.ChangeStateOpen,
		BaseSha:           "base1",
		HeadSha:           "head1",
		GitRef:            "refs/changes/1/head",
		Title:             "Add checkout retries",
		AuthoredByActorID: fx.ActorID,
	})
	if err != nil {
		t.Fatalf("seedChange: %v", err)
	}
	return change
}

// TestRerunCheckAgainstLivePostgres exercises the §14.4.2 re-run flow for
// real: the first CheckRun for a name is attempt 1, and RerunCheck must
// find it via GetLatestCheckRunAttempt and create attempt 2 linked to the
// same (change, head_sha, name) - not a fresh, unrelated row.
//
// Skips unless RUNKO_TEST_DATABASE_URL is set (see internal/dbtest, db/README.md).
func TestRerunCheckAgainstLivePostgres(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Connect(t)
	fx := dbtest.Seed(t, ctx, db, t.Name())
	q := dbgen.New()
	change := seedChange(t, ctx, db, q, fx)

	first, err := RerunCheck(ctx, db, q, change.ID, "head1", "unit:checkout-api", "ext-1", "github-actions")
	if err != nil {
		t.Fatalf("RerunCheck (first): %v", err)
	}
	if first.Attempt != 1 {
		t.Fatalf("expected the first CheckRun for a name to be attempt 1, got %d", first.Attempt)
	}

	second, err := RerunCheck(ctx, db, q, change.ID, "head1", "unit:checkout-api", "ext-2", "github-actions")
	if err != nil {
		t.Fatalf("RerunCheck (second): %v", err)
	}
	if second.Attempt != 2 {
		t.Fatalf("expected the second RerunCheck to be attempt 2, got %d", second.Attempt)
	}
	if second.ChangeID != first.ChangeID || second.HeadSha != first.HeadSha || second.Name != first.Name {
		t.Fatalf("expected the rerun to stay linked to the same (change, head_sha, name), got %+v vs %+v", first, second)
	}

	runs, err := q.ListCheckRunsForChange(ctx, db, dbgen.ListCheckRunsForChangeParams{ChangeID: change.ID, HeadSha: "head1"})
	if err != nil {
		t.Fatalf("ListCheckRunsForChange: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected both attempts to be visible as distinct rows, got %d", len(runs))
	}
}

// TestEnqueueAndRecordWebhookDeliveryAgainstLivePostgres exercises the
// §14.4.1 outbox lifecycle for real: enqueue creates a pending delivery,
// a failed attempt schedules a backoff retry (not dead-lettered before the
// threshold), and a subsequent success marks it delivered.
func TestEnqueueAndRecordWebhookDeliveryAgainstLivePostgres(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Connect(t)
	fx := dbtest.Seed(t, ctx, db, t.Name())
	q := dbgen.New()

	env := WebhookEnvelope{
		SpecVersion: "1.0", DeliveryID: "dlv_1", Type: "change.checks_required",
		OccurredAt: time.Unix(0, 0).UTC(),
		OrgID:      fx.OrgID.String(), MonorepoID: fx.MonorepoID.String(),
		Change: WebhookChange{ID: "chg_1", Number: 1, BaseSHA: "base1", HeadSHA: "head1"},
	}

	delivery, err := EnqueueWebhook(ctx, db, q, fx.OrgID, env)
	if err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}
	if delivery.Status != dbgen.WebhookDeliveryStatusPending {
		t.Fatalf("expected a freshly enqueued delivery to be pending, got %v", delivery.Status)
	}

	now := time.Now()
	if err := RecordDeliveryResult(ctx, db, q, delivery.ID, 1, DeliveryAttempt{Success: false, Err: errBoom}, time.Second, time.Minute, now); err != nil {
		t.Fatalf("RecordDeliveryResult (failure): %v", err)
	}
	afterFail, err := q.GetWebhookDelivery(ctx, db, delivery.ID)
	if err != nil {
		t.Fatalf("GetWebhookDelivery: %v", err)
	}
	if afterFail.Status != dbgen.WebhookDeliveryStatusFailed {
		t.Fatalf("expected status failed (not yet dead-lettered) after attempt 1 < MaxDeliveryAttempts, got %v", afterFail.Status)
	}
	if afterFail.LastError == nil || *afterFail.LastError == "" {
		t.Fatalf("expected last_error to be recorded")
	}
	if !afterFail.NextAttemptAt.Time.After(now) {
		t.Fatalf("expected next_attempt_at to be scheduled in the future, got %v (now=%v)", afterFail.NextAttemptAt.Time, now)
	}

	if err := RecordDeliveryResult(ctx, db, q, delivery.ID, 2, DeliveryAttempt{Success: true}, time.Second, time.Minute, now); err != nil {
		t.Fatalf("RecordDeliveryResult (success): %v", err)
	}
	delivered, err := q.GetWebhookDelivery(ctx, db, delivery.ID)
	if err != nil {
		t.Fatalf("GetWebhookDelivery after success: %v", err)
	}
	if delivered.Status != dbgen.WebhookDeliveryStatusDelivered {
		t.Fatalf("expected status delivered after a successful attempt, got %v", delivered.Status)
	}
}

func TestRecordDeliveryResultDeadLettersPastMaxAttemptsAgainstLivePostgres(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Connect(t)
	fx := dbtest.Seed(t, ctx, db, t.Name())
	q := dbgen.New()

	env := WebhookEnvelope{SpecVersion: "1.0", DeliveryID: "dlv_2", Type: "change.checks_required", OccurredAt: time.Unix(0, 0).UTC()}
	delivery, err := EnqueueWebhook(ctx, db, q, fx.OrgID, env)
	if err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}

	if err := RecordDeliveryResult(ctx, db, q, delivery.ID, MaxDeliveryAttempts, DeliveryAttempt{Success: false, Err: errBoom}, time.Second, time.Minute, time.Now()); err != nil {
		t.Fatalf("RecordDeliveryResult: %v", err)
	}
	row, err := q.GetWebhookDelivery(ctx, db, delivery.ID)
	if err != nil {
		t.Fatalf("GetWebhookDelivery: %v", err)
	}
	if row.Status != dbgen.WebhookDeliveryStatusDeadLetter {
		t.Fatalf("expected dead_letter once attempt reaches MaxDeliveryAttempts (%d), got %v", MaxDeliveryAttempts, row.Status)
	}
}
