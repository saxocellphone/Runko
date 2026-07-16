package runkod

// Land-stack tests (§13.5's "land everything through this Change" verb):
// the sweep lands the ancestor chain bottom-up through the exact same
// landChangeCore as single lands, stops at the first member whose gate
// refuses, resumes past already-landed members on the next call, and
// never touches dependents stacked above the requested Change.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/checks"
)

// pushStackedTriple extends pushStackedPair with Change C (stack_test.go's
// stackIDC) on top of B.
func pushStackedTriple(t *testing.T, bare string, store Store) (p *Processor, repo *gitfixture.Repo) {
	t.Helper()
	p, repo, _, _, headB := pushStackedPair(t, bare, store)
	ctx := context.Background()

	repo.WriteFile("proj/c.txt", "c\n")
	repo.Commit("change C\n\nChange-Id: " + stackIDC)
	_, headC := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: headB, NewSHA: headC, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push C rejected: %+v", res)
	}
	return p, repo
}

// TestLandStackLandsAncestorChain: land-stack on the MIDDLE of A→B→C lands
// A then B (bottom-up, attributed to the caller) and leaves C open; a
// follow-up land-stack on C lands just C; a third call is a no-op.
func TestLandStackLandsAncestorChain(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	pushStackedTriple(t, bare, store)
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, AllowUnpolicedLand: true}
	ctx := context.Background()
	val := &Principal{Name: "val", Stored: true}

	chB, _, _ := store.GetChange(ctx, stackIDB)
	dec, apiErr := srv.landStackCore(ctx, stackIDB, chB, nil, val)
	if apiErr != nil {
		t.Fatalf("land-stack through B: %+v", apiErr)
	}
	if dec.StoppedKey != "" {
		t.Fatalf("green chain must land through B, stopped at %s (blockers %v)", dec.StoppedKey, dec.Blockers)
	}
	if len(dec.Landed) != 2 || dec.Landed[0].ChangeKey != stackIDA || dec.Landed[1].ChangeKey != stackIDB {
		t.Fatalf("want [A B] landed bottom-up, got %+v", dec.Landed)
	}
	for _, id := range []string{stackIDA, stackIDB} {
		c, _, _ := store.GetChange(ctx, id)
		if c.State != "landed" || c.LandedBy != "val" {
			t.Fatalf("%s: want landed by val, got state=%q by=%q", id, c.State, c.LandedBy)
		}
	}
	// The dependent ABOVE the requested member is untouched.
	if c, _, _ := store.GetChange(ctx, stackIDC); c.State != "open" {
		t.Fatalf("C is above the land-through point and must stay open, got %q", c.State)
	}

	// Landing the leftover top: only C is left to land.
	chC, _, _ := store.GetChange(ctx, stackIDC)
	dec, apiErr = srv.landStackCore(ctx, stackIDC, chC, nil, val)
	if apiErr != nil || dec.StoppedKey != "" || len(dec.Landed) != 1 || dec.Landed[0].ChangeKey != stackIDC {
		t.Fatalf("land-stack on C: %+v %+v", dec, apiErr)
	}

	// Everything landed: the sweep is a no-op, not an error.
	chC, _, _ = store.GetChange(ctx, stackIDC)
	dec, apiErr = srv.landStackCore(ctx, stackIDC, chC, nil, val)
	if apiErr != nil || dec.StoppedKey != "" || len(dec.Landed) != 0 {
		t.Fatalf("no-op sweep: %+v %+v", dec, apiErr)
	}
}

// TestLandStackStopsAtBlockedMember: with a required check on the project,
// the sweep stops at the bottom-most blocked member without landing
// anything above it, reports that member's own blockers, and RESUMES past
// already-landed members once the gate goes green.
func TestLandStackStopsAtBlockedMember(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("proj/PROJECT.yaml",
		"schema: project/v1\nname: alpha\ntype: library\nci:\n  checks:\n    - name: unit\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	ctx := context.Background()

	repo.WriteFile("proj/a.txt", "a\n")
	repo.Commit("change A\n\nChange-Id: " + stackIDA)
	_, headA := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: oldSHA, NewSHA: headA, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push A rejected: %+v", res)
	}
	repo.WriteFile("proj/b.txt", "b\n")
	repo.Commit("change B\n\nChange-Id: " + stackIDB)
	_, headB := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: headA, NewSHA: headB, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push B rejected: %+v", res)
	}

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: p}

	// Both members' "unit" is unreported: the sweep must stop at A with
	// A's own blockers and land NOTHING.
	chB, _, _ := store.GetChange(ctx, stackIDB)
	dec, apiErr := srv.landStackCore(ctx, stackIDB, chB, nil, nil)
	if apiErr != nil {
		t.Fatalf("blocked sweep must report, not error: %+v", apiErr)
	}
	if len(dec.Landed) != 0 || dec.StoppedKey != stackIDA {
		t.Fatalf("want stop at A with nothing landed, got landed=%v stopped=%s", dec.Landed, dec.StoppedKey)
	}
	if len(dec.Blockers) == 0 || !strings.Contains(strings.Join(dec.Blockers, "; "), "unit") {
		t.Fatalf("stopped member's blockers should name the unreported check, got %v", dec.Blockers)
	}

	// A's gate goes green: the sweep lands A, then stops at B.
	chA, _, _ := store.GetChange(ctx, stackIDA)
	if err := store.UpsertCheckRun(ctx, stackIDA, chA.HeadSHA, checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report unit for A: %v", err)
	}
	chB, _, _ = store.GetChange(ctx, stackIDB)
	dec, apiErr = srv.landStackCore(ctx, stackIDB, chB, nil, nil)
	if apiErr != nil {
		t.Fatalf("half-green sweep: %+v", apiErr)
	}
	if len(dec.Landed) != 1 || dec.Landed[0].ChangeKey != stackIDA || dec.StoppedKey != stackIDB {
		t.Fatalf("want A landed and stop at B, got landed=%v stopped=%s", dec.Landed, dec.StoppedKey)
	}

	// B's gate goes green: the resumed sweep skips landed A and lands B.
	chB, _, _ = store.GetChange(ctx, stackIDB)
	if err := store.UpsertCheckRun(ctx, stackIDB, chB.HeadSHA, checks.CheckRunView{
		Name: "unit", Status: checks.CheckStatusCompleted, Conclusion: checks.ConclusionSuccess,
	}); err != nil {
		t.Fatalf("report unit for B: %v", err)
	}
	dec, apiErr = srv.landStackCore(ctx, stackIDB, chB, nil, nil)
	if apiErr != nil || dec.StoppedKey != "" || len(dec.Landed) != 1 || dec.Landed[0].ChangeKey != stackIDB {
		t.Fatalf("resumed sweep: %+v %+v", dec, apiErr)
	}
}

// TestLandStackRefusesAbandonedChange: the requested member itself must be
// landable state - the structured invalid_state 409, same as LandChange.
func TestLandStackRefusesAbandonedChange(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	pushStackedPair(t, bare, store)
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, AllowUnpolicedLand: true}
	ctx := context.Background()

	if _, err := store.MarkChangeAbandoned(ctx, stackIDB); err != nil {
		t.Fatalf("abandon B: %v", err)
	}
	chB, _, _ := store.GetChange(ctx, stackIDB)
	_, apiErr := srv.landStackCore(ctx, stackIDB, chB, nil, nil)
	if apiErr == nil || apiErr.Status != http.StatusConflict || apiErr.Err.Code != "invalid_state" {
		t.Fatalf("want 409 invalid_state, got %+v", apiErr)
	}
}
