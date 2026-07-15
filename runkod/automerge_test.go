package runkod

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/receive"
)

// automergeFixture: one open Change on a project declaring a required
// "unit" check (§14.9 ci.checks) - so the Change starts NOT mergeable and
// flips green exactly when the check reports success.
func automergeFixture(t *testing.T) (*Server, *AutomergeWorker, *MemStore, string) {
	t.Helper()
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nci:\n  checks:\n    - name: unit\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push rejected: %+v", result)
	}
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	worker := NewAutomergeWorker(srv, 0)
	return srv, worker, store, result.ChangeID
}

// TestAutomergeLandsWhenGateGoesGreen is the feature: armed while blocked,
// nothing happens; the required check reports success and the next sweep
// lands it - attributed to the ARMING principal.
func TestAutomergeLandsWhenGateGoesGreen(t *testing.T) {
	srv, worker, store, changeID := automergeFixture(t)
	ctx := context.Background()

	armed, apiErr := srv.setAutomergeCore(ctx, changeID, true, &Principal{Name: "val", Stored: true})
	if apiErr != nil {
		t.Fatalf("arm: %+v", apiErr)
	}
	if !armed.Automerge || armed.AutomergeBy != "val" {
		t.Fatalf("armed state: %+v", armed)
	}

	// Blocked (unit not reported): the sweep must do nothing.
	worker.SweepOnce(ctx)
	if c, _, _ := store.GetChange(ctx, changeID); c.State != "open" {
		t.Fatalf("blocked change must not land, got %q", c.State)
	}

	// The gate goes green - the sweep lands it.
	if err := store.UpsertCheckRun(ctx, changeID, armed.HeadSHA, checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report unit: %v", err)
	}
	worker.SweepOnce(ctx)
	landed, _, _ := store.GetChange(ctx, changeID)
	if landed.State != "landed" {
		t.Fatalf("armed+green must land, got %q", landed.State)
	}
	if landed.LandedBy != "val" {
		t.Fatalf("the automatic land is attributed to the ARMER, got %q", landed.LandedBy)
	}
}

// TestAutomergeDisarmAndStateGuards: disarm sticks; arming a non-open
// change is refused; an unarmed green change never lands by itself.
func TestAutomergeDisarmAndStateGuards(t *testing.T) {
	srv, worker, store, changeID := automergeFixture(t)
	ctx := context.Background()

	if _, apiErr := srv.setAutomergeCore(ctx, changeID, true, nil); apiErr != nil {
		t.Fatalf("arm: %+v", apiErr)
	}
	if disarmed, apiErr := srv.setAutomergeCore(ctx, changeID, false, nil); apiErr != nil || disarmed.Automerge {
		t.Fatalf("disarm: %+v %+v", disarmed, apiErr)
	}

	// Green but DISARMED: the sweep must leave it alone.
	c, _, _ := store.GetChange(ctx, changeID)
	if err := store.UpsertCheckRun(ctx, changeID, c.HeadSHA, checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report: %v", err)
	}
	worker.SweepOnce(ctx)
	if c, _, _ := store.GetChange(ctx, changeID); c.State != "open" {
		t.Fatalf("disarmed change must never auto-land, got %q", c.State)
	}

	// Abandoned: arming is refused with the structured 409.
	if _, err := store.MarkChangeAbandoned(ctx, changeID); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	if _, apiErr := srv.setAutomergeCore(ctx, changeID, true, nil); apiErr == nil || apiErr.Err.Code != "invalid_state" {
		t.Fatalf("arming an abandoned change: want invalid_state, got %+v", apiErr)
	}
	// Disarming a non-open change stays allowed (cleanup is never blocked).
	if _, apiErr := srv.setAutomergeCore(ctx, changeID, false, nil); apiErr != nil {
		t.Fatalf("disarm on abandoned: %+v", apiErr)
	}
}

// TestAutomergeKickOnCheckReport: the REST report path kicks the worker,
// so an armed change lands on the report that greened it - no sweep-tick
// latency. (Drives the real HTTP handler; the worker runs in background.)
func TestAutomergeKickOnCheckReport(t *testing.T) {
	srv, worker, store, changeID := automergeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	handler, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	hs := httptest.NewServer(handler)
	defer hs.Close()

	if _, apiErr := srv.setAutomergeCore(ctx, changeID, true, &Principal{Name: "val", Stored: true}); apiErr != nil {
		t.Fatalf("arm: %+v", apiErr)
	}

	body := `{"name":"unit","external_id":"r1","status":"completed","conclusion":"success","reporter":"ci"}`
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/changes/"+changeID+"/checks", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sekret")
	req.Header.Set("Content-Type", "application/json")
	resp, err := hs.Client().Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("report: %v %v", err, resp)
	}
	resp.Body.Close()

	// The kick makes this prompt; poll briefly rather than flake.
	for i := 0; i < 50; i++ {
		if c, _, _ := store.GetChange(ctx, changeID); c.State == "landed" {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	c, _, _ := store.GetChange(ctx, changeID)
	t.Fatalf("armed change must land promptly after the greening report, still %q", c.State)
}

// Under the conflict-only default (§13.5, 2026-07-15), an armed green
// change lands on the next sweep even when trunk advanced with an
// INTERSECTING delta - the worker never parks on requires_revalidation.
func TestAutomergeLandsAcrossIntersectingTrunkMoveUnderConflictOnly(t *testing.T) {
	srv, worker, store, changeID := automergeFixture(t)
	ctx := context.Background()

	if _, apiErr := srv.setAutomergeCore(ctx, changeID, true, &Principal{Name: "val", Stored: true}); apiErr != nil {
		t.Fatalf("arm: %+v", apiErr)
	}
	armed, _, _ := store.GetChange(ctx, changeID)
	if err := store.UpsertCheckRun(ctx, changeID, armed.HeadSHA, checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report unit: %v", err)
	}

	// Move trunk with a SAME-PROJECT (intersecting), different-file commit.
	repo := gitfixture.New(t)
	repo.Run("fetch " + srv.RepoDir + " refs/heads/main")
	repo.Run("checkout -q FETCH_HEAD")
	repo.WriteFile("commerce/checkout/other.go", "package main\n// trunk moved\n")
	trunkTip := repo.Commit("trunk touches checkout too")
	if _, err := gitfixtureRunGit(repo.Dir, "push", "-f", srv.RepoDir, trunkTip+":refs/heads/main"); err != nil {
		t.Fatalf("advance trunk: %v", err)
	}

	worker.SweepOnce(ctx)
	landed, _, _ := store.GetChange(ctx, changeID)
	if landed.State != "landed" {
		t.Fatalf("armed+green must land across an intersecting trunk move under conflict-only, got %q", landed.State)
	}
}
