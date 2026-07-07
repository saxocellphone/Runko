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

// TestPostgresStoreApprovalRoundTrip exercises stage 2's
// change_owner_requirements table through its first real caller (§28.3
// stage 11c): recording an approval creates the requirement row, satisfies
// it, and attributes it to a real actors row (external_ref = approved_by),
// which ListApprovals resolves back to the name.
func TestPostgresStoreApprovalRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title"); err != nil {
		t.Fatalf("CreateOrUpdateChange: %v", err)
	}

	approvals, err := store.ListApprovals(ctx, "Iabc")
	if err != nil {
		t.Fatalf("ListApprovals (empty): %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("expected no approvals yet, got %+v", approvals)
	}

	if err := store.RecordApproval(ctx, "Iabc", "group:commerce-eng", "alice"); err != nil {
		t.Fatalf("RecordApproval: %v", err)
	}
	// Idempotent: approving the same ref again is not an error.
	if err := store.RecordApproval(ctx, "Iabc", "group:commerce-eng", "alice"); err != nil {
		t.Fatalf("RecordApproval (repeat): %v", err)
	}

	approvals, err = store.ListApprovals(ctx, "Iabc")
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 1 || approvals[0].OwnerRef != "group:commerce-eng" || approvals[0].ApprovedBy != "alice" {
		t.Fatalf("expected one approval by alice for group:commerce-eng, got %+v", approvals)
	}
}

// TestPostgresStoreWorkspaceRoundTrip exercises stage 2's workspaces table
// through its first real caller (§28.3 stage 12b). The human workspace ID
// lives inside snapshot_ref (no dedicated column), so this also pins the
// derive-both-ways mapping dbWorkspaceToWorkspace does.
func TestPostgresStoreWorkspaceRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	ws := Workspace{
		ID: "payments-fix", Owner: "alice",
		BaseRevision:    "abc123",
		ProjectAffinity: []string{"checkout-api"},
		WriteAllowlist:  []string{"commerce/checkout"},
		SnapshotRef:     "refs/workspaces/payments-fix/head",
		Status:          "active",
	}
	created, err := store.CreateWorkspace(ctx, ws)
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if created.ID != "payments-fix" || created.Owner != "alice" || created.Status != "active" {
		t.Fatalf("unexpected created workspace: %+v", created)
	}

	if _, err := store.CreateWorkspace(ctx, ws); err == nil {
		t.Fatalf("expected a duplicate workspace ID to error")
	}

	got, ok, err := store.GetWorkspace(ctx, "payments-fix")
	if err != nil || !ok {
		t.Fatalf("GetWorkspace: ok=%v err=%v", ok, err)
	}
	if got.BaseRevision != "abc123" || len(got.WriteAllowlist) != 1 || got.WriteAllowlist[0] != "commerce/checkout" {
		t.Fatalf("unexpected workspace: %+v", got)
	}
	if _, ok, err := store.GetWorkspace(ctx, "no-such"); err != nil || ok {
		t.Fatalf("expected ok=false for an unknown workspace, got ok=%v err=%v", ok, err)
	}

	// A second workspace with EMPTY affinity/allowlist pins the nil->[]
	// normalization (the stage-9a index.Sync NOT NULL lesson).
	if _, err := store.CreateWorkspace(ctx, Workspace{
		ID: "bare-ws", Owner: "alice", BaseRevision: "abc123",
		SnapshotRef: "refs/workspaces/bare-ws/head", Status: "active",
	}); err != nil {
		t.Fatalf("CreateWorkspace with nil slices: %v", err)
	}

	list, err := store.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaces: %v", err)
	}
	if len(list) != 2 || list[0].ID != "bare-ws" || list[1].ID != "payments-fix" {
		t.Fatalf("unexpected list: %+v", list)
	}

	updated, err := store.UpdateWorkspaceBase(ctx, "payments-fix", "def456")
	if err != nil || updated.BaseRevision != "def456" {
		t.Fatalf("UpdateWorkspaceBase: %+v err=%v", updated, err)
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
