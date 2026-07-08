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

	created, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title", "", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (create): %v", err)
	}
	if created.State != "open" {
		t.Fatalf("expected a new Change to start open, got %+v", created)
	}

	updated, err := store.CreateOrUpdateChange(ctx, "Iabc", "base2", "head2", "refs/changes/1/head", "title", "", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (update): %v", err)
	}
	if updated.HeadSHA != "head2" {
		t.Fatalf("expected head_sha to advance, got %+v", updated)
	}
	if updated.BaseSHA != "base2" {
		t.Fatalf("expected base_sha to move with the amend (edge case E7: a frozen base makes requires_revalidation permanent), got %+v", updated)
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
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title", "", "", ""); err != nil {
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
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head1", "refs/changes/1/head", "title", "", "", ""); err != nil {
		t.Fatalf("CreateOrUpdateChange: %v", err)
	}

	approvals, err := store.ListApprovals(ctx, "Iabc")
	if err != nil {
		t.Fatalf("ListApprovals (empty): %v", err)
	}
	if len(approvals) != 0 {
		t.Fatalf("expected no approvals yet, got %+v", approvals)
	}

	if err := store.RecordApproval(ctx, "Iabc", "group:commerce-eng", "alice", "head1"); err != nil {
		t.Fatalf("RecordApproval: %v", err)
	}
	// Idempotent: approving the same ref again is not an error.
	if err := store.RecordApproval(ctx, "Iabc", "group:commerce-eng", "alice", "head1"); err != nil {
		t.Fatalf("RecordApproval (repeat): %v", err)
	}

	approvals, err = store.ListApprovals(ctx, "Iabc")
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 1 || approvals[0].OwnerRef != "group:commerce-eng" || approvals[0].ApprovedBy != "alice" {
		t.Fatalf("expected one approval by alice for group:commerce-eng, got %+v", approvals)
	}
	// §13.5: the approval round-trips WITH the head it was granted for -
	// the merge gate's staleness comparison depends on this surviving
	// Postgres, not just MemStore.
	if approvals[0].HeadSHA != "head1" {
		t.Fatalf("expected the approval bound to head1, got %q", approvals[0].HeadSHA)
	}

	// Re-approving after an amend re-binds the row to the new head (the
	// PK is (change_id, owner_ref) - one row per owner, latest head wins).
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "base1", "head2", "refs/changes/1/head", "title", "", "", ""); err != nil {
		t.Fatalf("CreateOrUpdateChange (amend): %v", err)
	}
	if err := store.RecordApproval(ctx, "Iabc", "group:commerce-eng", "alice", "head2"); err != nil {
		t.Fatalf("RecordApproval (re-approve): %v", err)
	}
	approvals, err = store.ListApprovals(ctx, "Iabc")
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(approvals) != 1 || approvals[0].HeadSHA != "head2" {
		t.Fatalf("expected the re-approval bound to head2, got %+v", approvals)
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

// TestPostgresStoreAttributionRoundTrip pins stage 12c-②'s authored_by/
// landed_by attribution through real actors rows: named principals become
// upserted actors, the anonymous deploy token ("") maps to the bootstrap
// placeholder and reads back as "", and an amend moves authored_by to the
// last pusher (who self-approval is checked against).
func TestPostgresStoreAttributionRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	created, err := store.CreateOrUpdateChange(ctx, "Iattr", "base1", "head1", "refs/changes/2/head", "title", "alice", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange: %v", err)
	}
	if created.AuthoredBy != "alice" {
		t.Fatalf("expected AuthoredBy=alice, got %q", created.AuthoredBy)
	}

	// Amend by a different principal: last pusher owns the head.
	updated, err := store.CreateOrUpdateChange(ctx, "Iattr", "base1", "head2", "refs/changes/2/head", "title", "bob", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (amend): %v", err)
	}
	if updated.AuthoredBy != "bob" {
		t.Fatalf("expected AuthoredBy to move to bob on amend, got %q", updated.AuthoredBy)
	}

	landed, err := store.MarkChangeLanded(ctx, "Iattr", "head2", "carol", true)
	if err != nil {
		t.Fatalf("MarkChangeLanded: %v", err)
	}
	if landed.LandedBy != "carol" || landed.State != "landed" {
		t.Fatalf("expected landed by carol, got %+v", landed)
	}
	if !landed.LandedForced {
		t.Fatalf("expected landed_forced audit bit to persist and read back, got %+v", landed)
	}

	// Anonymous stays anonymous, both fields.
	anon, err := store.CreateOrUpdateChange(ctx, "Ianon", "base1", "head1", "refs/changes/3/head", "title", "", "", "")
	if err != nil {
		t.Fatalf("CreateOrUpdateChange (anon): %v", err)
	}
	if anon.AuthoredBy != "" {
		t.Fatalf("expected anonymous AuthoredBy to read back empty, got %q", anon.AuthoredBy)
	}
	if _, err := store.MarkChangeLanded(ctx, "Ianon", "head1", "", false); err != nil {
		t.Fatalf("MarkChangeLanded (anon): %v", err)
	}
	got, ok, err := store.GetChange(ctx, "Ianon")
	if err != nil || !ok {
		t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}
	if got.LandedForced {
		t.Fatalf("expected an ordinary land to read back landed_forced=false, got %+v", got)
	}
	if got.LandedBy != "" {
		t.Fatalf("expected anonymous LandedBy to read back empty, got %q", got.LandedBy)
	}
}

// TestPostgresStoreLifecycleAndRerunAttempts covers stage 12c-③'s Postgres
// side: list/abandon/reopen state transitions, and - the subtle one - the
// attempt semantics around reruns: RerunCheck creates attempt N+1, a result
// posted AFTERWARDS must complete the rerun's attempt (UpsertCheckRun now
// resolves the latest attempt; it used to hardcode attempt 1, which would
// have stranded every rerun as forever-queued), and ListCheckRuns collapses
// history to one latest-attempt view per name.
func TestPostgresStoreLifecycleAndRerunAttempts(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	if _, err := store.CreateOrUpdateChange(ctx, "I1", "b", "h1", "r1", "t1", "", "", ""); err != nil {
		t.Fatalf("create I1: %v", err)
	}
	if _, err := store.CreateOrUpdateChange(ctx, "I2", "b", "h2", "r2", "t2", "", "", ""); err != nil {
		t.Fatalf("create I2: %v", err)
	}

	open, err := store.ListChanges(ctx, "open")
	if err != nil || len(open) != 2 {
		t.Fatalf("expected 2 open changes, got %v (%v)", open, err)
	}
	if open[0].ChangeKey != "I2" {
		t.Fatalf("expected newest-first ordering (I2 has the higher number), got %+v", open)
	}

	// Abandon -> filtered lists reflect it; idempotent; re-push reopens.
	if _, err := store.MarkChangeAbandoned(ctx, "I1"); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if _, err := store.MarkChangeAbandoned(ctx, "I1"); err != nil {
		t.Fatalf("abandon (idempotent): %v", err)
	}
	if abandoned, err := store.ListChanges(ctx, "abandoned"); err != nil || len(abandoned) != 1 || abandoned[0].ChangeKey != "I1" {
		t.Fatalf("expected I1 abandoned, got %v (%v)", abandoned, err)
	}
	reopened, err := store.CreateOrUpdateChange(ctx, "I1", "b", "h3", "r1", "t1", "", "", "")
	if err != nil || reopened.State != "open" {
		t.Fatalf("expected re-push to reopen, got %+v (%v)", reopened, err)
	}

	// Landed is terminal.
	if _, err := store.MarkChangeLanded(ctx, "I2", "h2", "", false); err != nil {
		t.Fatalf("land I2: %v", err)
	}
	if _, err := store.MarkChangeAbandoned(ctx, "I2"); err == nil {
		t.Fatalf("expected abandoning a landed change to error")
	}

	// Rerun attempt semantics on I1@h3.
	if err := store.UpsertCheckRun(ctx, "I1", "h3", checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report attempt 1: %v", err)
	}
	rerun, err := store.RerunCheck(ctx, "I1", "unit", "alice")
	if err != nil {
		t.Fatalf("RerunCheck: %v", err)
	}
	if rerun.Status != checks.CheckStatusQueued {
		t.Fatalf("expected the rerun view queued, got %+v", rerun)
	}
	runs, err := store.ListCheckRuns(ctx, "I1", "h3")
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected ONE latest-attempt view after rerun, got %v (%v)", runs, err)
	}
	if runs[0].Status != checks.CheckStatusQueued {
		t.Fatalf("expected the latest attempt (queued) to win, got %+v", runs[0])
	}
	if runs[0].LastSeenAt.IsZero() || runs[0].TTLSeconds <= 0 {
		t.Fatalf("expected staleness inputs populated from Postgres, got %+v", runs[0])
	}

	// A result posted after the rerun completes the RERUN's attempt.
	if err := store.UpsertCheckRun(ctx, "I1", "h3", checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report attempt 2: %v", err)
	}
	runs, err = store.ListCheckRuns(ctx, "I1", "h3")
	if err != nil || len(runs) != 1 {
		t.Fatalf("expected one view, got %v (%v)", runs, err)
	}
	if runs[0].Status != checks.CheckStatusCompleted || runs[0].Conclusion != checks.ConclusionSuccess {
		t.Fatalf("expected the rerun attempt completed (not attempt 1 resurrected), got %+v", runs[0])
	}
}

func TestPostgresStorePrincipalRoundTrip(t *testing.T) {
	store := newTestPostgresStore(t)

	if _, found, err := store.GetStoredPrincipal(context.Background(), "val"); err != nil || found {
		t.Fatalf("empty table: found=%v err=%v", found, err)
	}
	if err := store.CreatePrincipal(context.Background(), "val", "pbkdf2-sha256$1$c2FsdA$aGFzaA"); err != nil {
		t.Fatalf("CreatePrincipal: %v", err)
	}
	sp, found, err := store.GetStoredPrincipal(context.Background(), "val")
	if err != nil || !found || sp.Name != "val" || sp.CredentialHash != "pbkdf2-sha256$1$c2FsdA$aGFzaA" {
		t.Fatalf("round trip: %+v found=%v err=%v", sp, found, err)
	}
	// The unique constraint backs the handler's race path.
	if err := store.CreatePrincipal(context.Background(), "val", "other"); err == nil {
		t.Fatalf("duplicate CreatePrincipal should error")
	}
}

// TestPostgresStoreMirrorCursors round-trips the §18.6 cursor state:
// upsert (records sync + clears frozen), freeze, list.
func TestPostgresStoreMirrorCursors(t *testing.T) {
	store := newTestPostgresStore(t)
	ctx := context.Background()

	if _, ok, err := store.GetMirrorCursor(ctx, "mirror", "refs/heads/main"); err != nil || ok {
		t.Fatalf("empty cursor: ok=%v err=%v", ok, err)
	}
	if err := store.UpsertMirrorCursor(ctx, "mirror", "refs/heads/main", "sha1"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.FreezeMirrorCursor(ctx, "mirror", "refs/heads/main"); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	c, ok, err := store.GetMirrorCursor(ctx, "mirror", "refs/heads/main")
	if err != nil || !ok || !c.Frozen || c.LastSyncedSHA != "sha1" {
		t.Fatalf("frozen cursor: %+v ok=%v err=%v", c, ok, err)
	}
	// Upsert IS the unfreeze (the admin action re-points and thaws in one).
	if err := store.UpsertMirrorCursor(ctx, "mirror", "refs/heads/main", "sha2"); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	list, err := store.ListMirrorCursors(ctx, "mirror")
	if err != nil || len(list) != 1 || list[0].Frozen || list[0].LastSyncedSHA != "sha2" {
		t.Fatalf("list after thaw: %+v err=%v", list, err)
	}
}
