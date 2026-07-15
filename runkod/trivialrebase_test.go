package runkod

// §13.5 trivial-rebase carry-forward tests (2026-07-15): the detector's
// truth table against real git, then the two integration paths - a client
// re-push through the funnel and a server-side stack sync - including the
// policy-interaction rule (checks carry only under conflict-only) and the
// webhook suppression for fully-covered heads.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/land"
)

// trivialFixture seeds trunk with a project declaring one check, pushes a
// change, and returns everything the detector/carry tests need. The repo is
// parked on the change's head.
func trivialFixture(t *testing.T) (p *Processor, store *MemStore, repo *gitfixture.Repo, bare, trunkTip, headSHA, changeID string) {
	t.Helper()
	bare = newBareRepo(t)
	repo = gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nci:\n  checks:\n    - name: unit\n")
	trunkTip = repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/feature.go", "package main\n")
	repo.Commit("add feature\n\nChange-Id: Iabc123abc123abc123abc123abc123abc123abc1")
	_, headSHA = pushCommit(t, repo, bare, "refs/for/main")

	store = NewMemStore()
	p = newTestProcessor(bare, store)
	result := p.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push rejected: %+v", result)
	}
	return p, store, repo, bare, trunkTip, headSHA, result.ChangeID
}

// advanceTrunk lands an independent commit (disjoint file) on trunk and
// returns the new tip, leaving the fixture repo back on the change head.
func advanceTrunk(t *testing.T, repo *gitfixture.Repo, bare, from, headSHA string) string {
	t.Helper()
	repo.Run("checkout --detach " + from)
	repo.WriteFile("libs/other.txt", "trunk moves\n")
	repo.Commit("independent land")
	_, newTip := pushCommit(t, repo, bare, "refs/heads/main")
	repo.Run("checkout --detach " + headSHA)
	return newTip
}

func TestTrivialRebaseDetector(t *testing.T) {
	p, store, repo, bare, trunkTip, headSHA, changeID := trivialFixture(t)
	ctx := context.Background()
	old, _, _ := store.GetChange(ctx, changeID)
	newTip := advanceTrunk(t, repo, bare, trunkTip, headSHA)

	t.Run("clean rebase is trivial", func(t *testing.T) {
		mustRepoGit(t, repo, "rebase", "--onto", newTip, old.BaseSHA, headSHA)
		rebased, _ := gitfixtureRunGit(repo.Dir, "rev-parse", "HEAD")
		pushCommit(t, repo, bare, "refs/for/main")
		if !p.trivialRebaseOf(old, newTip, rebased, nil) {
			t.Fatalf("a clean rebase must read as trivial")
		}
		repo.Run("checkout --detach " + headSHA)
	})

	t.Run("content amend is not", func(t *testing.T) {
		repo.WriteFile("commerce/checkout/feature.go", "package main\n// v2\n")
		mustRepoGit(t, repo, "add", "-A")
		mustRepoGit(t, repo, "commit", "--amend", "--no-edit")
		amended, _ := gitfixtureRunGit(repo.Dir, "rev-parse", "HEAD")
		pushCommit(t, repo, bare, "refs/for/main")
		if p.trivialRebaseOf(old, old.BaseSHA, amended, nil) {
			t.Fatalf("a content amend must not read as trivial")
		}
		repo.Run("checkout --detach " + headSHA)
	})

	t.Run("message change is not", func(t *testing.T) {
		mustRepoGit(t, repo, "commit", "--amend", "-m", "renamed\n\nChange-Id: Iabc123abc123abc123abc123abc123abc123abc1")
		renamed, _ := gitfixtureRunGit(repo.Dir, "rev-parse", "HEAD")
		pushCommit(t, repo, bare, "refs/for/main")
		if p.trivialRebaseOf(old, old.BaseSHA, renamed, nil) {
			t.Fatalf("a message change must not read as trivial")
		}
		repo.Run("checkout --detach " + headSHA)
	})

	t.Run("same-head re-push is not", func(t *testing.T) {
		if p.trivialRebaseOf(old, old.BaseSHA, old.HeadSHA, nil) {
			t.Fatalf("a same-head re-push stays the documented CI re-trigger")
		}
	})

	t.Run("conflict-resolved rebase is not", func(t *testing.T) {
		// Trunk rewrites the SAME file the change touches, then the rebase
		// resolves the conflict - the delta changed, nothing may carry.
		repo.Run("checkout --detach " + newTip)
		repo.WriteFile("commerce/checkout/feature.go", "package main\n// trunk's own version\n")
		conflictTip := repo.Commit("trunk rewrites feature.go")
		pushCommit(t, repo, bare, "refs/heads/main")
		repo.Run("checkout --detach " + conflictTip)
		repo.WriteFile("commerce/checkout/feature.go", "package main\n// resolved\n")
		resolved := repo.Commit("add feature\n\nChange-Id: Iabc123abc123abc123abc123abc123abc123abc1")
		pushCommit(t, repo, bare, "refs/for/main")
		if p.trivialRebaseOf(old, conflictTip, resolved, nil) {
			t.Fatalf("a conflict-resolved rebase must not read as trivial")
		}
	})
}

// The everyday flow: trunk moved, `change push` auto-syncs (a local rebase)
// and re-pushes - approvals and passing checks ride to the new head and the
// push emits NO change.updated webhook (CI does not re-run).
func TestPushTrivialRebaseCarriesForward(t *testing.T) {
	p, store, repo, bare, trunkTip, headSHA, changeID := trivialFixture(t)
	ctx := context.Background()
	old, _, _ := store.GetChange(ctx, changeID)

	if err := store.UpsertCheckRun(ctx, changeID, headSHA, checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report unit: %v", err)
	}
	if err := store.RecordApproval(ctx, changeID, "commerce/checkout", "val", headSHA); err != nil {
		t.Fatalf("approve: %v", err)
	}
	webhooksBefore := countWebhooks(t, store)

	newTip := advanceTrunk(t, repo, bare, trunkTip, headSHA)
	mustRepoGit(t, repo, "rebase", "--onto", newTip, old.BaseSHA, headSHA)
	rebased, _ := gitfixtureRunGit(repo.Dir, "rev-parse", "HEAD")
	_, _ = pushCommit(t, repo, bare, "refs/for/main")

	result := p.Process(ctx, RefUpdate{OldSHA: headSHA, NewSHA: rebased, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("re-push rejected: %+v", result)
	}
	if !strings.Contains(result.Message, "checks carried forward") {
		t.Fatalf("expected the carried-forward remote line, got:\n%s", result.Message)
	}

	runs, _ := store.ListCheckRuns(ctx, changeID, rebased)
	if len(runs) != 1 || runs[0].Conclusion != checks.ConclusionSuccess || runs[0].CopiedFromHeadSHA != headSHA {
		t.Fatalf("expected the passing check carried with provenance, got %+v", runs)
	}
	carriedApproval := false
	for _, a := range mustListApprovals(ctx, store, changeID) {
		if a.HeadSHA == rebased && a.OwnerRef == "commerce/checkout" {
			carriedApproval = true
		}
	}
	if !carriedApproval {
		t.Fatalf("expected the approval carried to the new head")
	}
	if got := countWebhooks(t, store); got != webhooksBefore {
		t.Fatalf("a fully-covered carried head must emit NO change.updated, got %d new webhook(s)", got-webhooksBefore)
	}
}

// A content amend resets everything: no carried checks, no carried
// approvals, and the webhook fires as today.
func TestPushContentAmendCopiesNothing(t *testing.T) {
	p, store, repo, bare, _, headSHA, changeID := trivialFixture(t)
	ctx := context.Background()

	if err := store.UpsertCheckRun(ctx, changeID, headSHA, checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report unit: %v", err)
	}
	webhooksBefore := countWebhooks(t, store)

	repo.WriteFile("commerce/checkout/feature.go", "package main\n// v2\n")
	mustRepoGit(t, repo, "add", "-A")
	mustRepoGit(t, repo, "commit", "--amend", "--no-edit")
	amended, _ := gitfixtureRunGit(repo.Dir, "rev-parse", "HEAD")
	pushCommit(t, repo, bare, "refs/for/main")

	result := p.Process(ctx, RefUpdate{OldSHA: headSHA, NewSHA: amended, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("amend push rejected: %+v", result)
	}
	if strings.Contains(result.Message, "carried forward") {
		t.Fatalf("an amend must not carry, got:\n%s", result.Message)
	}
	if runs, _ := store.ListCheckRuns(ctx, changeID, amended); len(runs) != 0 {
		t.Fatalf("an amended head must start with zero check runs, got %+v", runs)
	}
	if got := countWebhooks(t, store); got != webhooksBefore+1 {
		t.Fatalf("an amend must emit change.updated as today, got %d new webhook(s)", got-webhooksBefore)
	}
}

// The policy-interaction rule: under an org-tightened affected-intersection
// tier, a trivial rebase still carries APPROVALS but never checks - carried
// checks would launder the intersecting trunk delta past the re-run the org
// opted into.
func TestPushUnderAffectedIntersectionCarriesApprovalsNotChecks(t *testing.T) {
	p, store, repo, bare, trunkTip, headSHA, changeID := trivialFixture(t)
	p.Revalidation = land.RevalidationAffectedIntersection
	ctx := context.Background()
	old, _, _ := store.GetChange(ctx, changeID)

	if err := store.UpsertCheckRun(ctx, changeID, headSHA, checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report unit: %v", err)
	}
	if err := store.RecordApproval(ctx, changeID, "commerce/checkout", "val", headSHA); err != nil {
		t.Fatalf("approve: %v", err)
	}
	webhooksBefore := countWebhooks(t, store)

	newTip := advanceTrunk(t, repo, bare, trunkTip, headSHA)
	mustRepoGit(t, repo, "rebase", "--onto", newTip, old.BaseSHA, headSHA)
	rebased, _ := gitfixtureRunGit(repo.Dir, "rev-parse", "HEAD")
	pushCommit(t, repo, bare, "refs/for/main")

	result := p.Process(ctx, RefUpdate{OldSHA: headSHA, NewSHA: rebased, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("re-push rejected: %+v", result)
	}
	if runs, _ := store.ListCheckRuns(ctx, changeID, rebased); len(runs) != 0 {
		t.Fatalf("checks must NOT carry under affected-intersection, got %+v", runs)
	}
	carriedApproval := false
	for _, a := range mustListApprovals(ctx, store, changeID) {
		if a.HeadSHA == rebased {
			carriedApproval = true
		}
	}
	if !carriedApproval {
		t.Fatalf("approvals carry under every tier")
	}
	if got := countWebhooks(t, store); got != webhooksBefore+1 {
		t.Fatalf("an uncovered head must still emit change.updated, got %d new webhook(s)", got-webhooksBefore)
	}
}

// Server-side stack sync: the Sync button's rebase carries checks +
// approvals per member, suppresses the webhook for covered heads, and
// kicks the automerge worker so an armed change lands without waiting for
// the sweep.
func TestSyncCarriesChecksAndApprovalsForward(t *testing.T) {
	p, store, repo, bare, trunkTip, headSHA, changeID := trivialFixture(t)
	ctx := context.Background()

	if err := store.UpsertCheckRun(ctx, changeID, headSHA, checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report unit: %v", err)
	}
	if err := store.RecordApproval(ctx, changeID, "commerce/checkout", "val", headSHA); err != nil {
		t.Fatalf("approve: %v", err)
	}
	webhooksBefore := countWebhooks(t, store)
	advanceTrunk(t, repo, bare, trunkTip, headSHA)

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: p, AllowUnpolicedLand: true}
	change, _, _ := store.GetChange(ctx, changeID)
	dec, apiErr := srv.syncChangeCore(ctx, changeID, change, nil)
	if apiErr != nil || !dec.Synced {
		t.Fatalf("sync: %+v %+v", dec, apiErr)
	}

	synced, _, _ := store.GetChange(ctx, changeID)
	runs, _ := store.ListCheckRuns(ctx, changeID, synced.HeadSHA)
	if len(runs) != 1 || runs[0].CopiedFromHeadSHA != headSHA {
		t.Fatalf("expected the check carried across the sync, got %+v", runs)
	}
	carriedApproval := false
	for _, a := range mustListApprovals(ctx, store, changeID) {
		if a.HeadSHA == synced.HeadSHA {
			carriedApproval = true
		}
	}
	if !carriedApproval {
		t.Fatalf("expected the approval carried across the sync")
	}
	if got := countWebhooks(t, store); got != webhooksBefore {
		t.Fatalf("a covered synced head must emit NO change.updated, got %d new webhook(s)", got-webhooksBefore)
	}
}

// Partial coverage fails closed: a required check with no passing result at
// the old head means the webhook fires and CI re-runs.
func TestSyncPartialCoverageStillEnqueuesWebhook(t *testing.T) {
	p, store, repo, bare, trunkTip, headSHA, changeID := trivialFixture(t)
	ctx := context.Background()
	// "unit" is required by the manifest but never reported - nothing to
	// carry, so the synced head is uncovered.
	webhooksBefore := countWebhooks(t, store)
	advanceTrunk(t, repo, bare, trunkTip, headSHA)

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: p, AllowUnpolicedLand: true}
	change, _, _ := store.GetChange(ctx, changeID)
	if dec, apiErr := srv.syncChangeCore(ctx, changeID, change, nil); apiErr != nil || !dec.Synced {
		t.Fatalf("sync: %+v %+v", dec, apiErr)
	}
	if got := countWebhooks(t, store); got != webhooksBefore+1 {
		t.Fatalf("an uncovered synced head must emit change.updated, got %d new webhook(s)", got-webhooksBefore)
	}
}

// countWebhooks counts due outbox rows - the CI trigger the §13.5
// carry-forward suppresses for covered heads.
func countWebhooks(t *testing.T, store *MemStore) int {
	t.Helper()
	pending, err := store.ListDueWebhookDeliveries(context.Background(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries: %v", err)
	}
	return len(pending)
}
