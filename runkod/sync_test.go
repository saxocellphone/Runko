package runkod

// Server-side stack sync tests (sync.go): trunk moves underneath a pushed
// stack, the Change page's Sync button rebases the whole stack onto the
// new tip without a workspace checkout. The fixtures drive real pushes
// through the Processor and real trunk movement on the bare repo, so what
// is asserted is what production git actually produces.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

func TestSyncChangeRebasesWholeStackOntoNewTrunk(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	p, repo, trunkTip, headA, headB := pushStackedPair(t, bare, store)
	ctx := context.Background()

	// Trunk moves: an independent commit on top of the old tip.
	repo.Run("checkout --detach " + trunkTip)
	repo.WriteFile("other/note.txt", "trunk moves\n")
	repo.Commit("independent land")
	_, newTip := pushCommit(t, repo, bare, "refs/heads/main")
	repo.Run("checkout main")

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: p, AllowUnpolicedLand: true}

	// Sync from the MIDDLE of the stack: the whole stack moves, wherever
	// the button was pressed.
	chB, _, _ := store.GetChange(ctx, stackIDB)
	dec, apiErr := srv.syncChangeCore(ctx, stackIDB, chB, nil)
	if apiErr != nil {
		t.Fatalf("sync: %+v", apiErr)
	}
	if !dec.Synced || dec.AlreadyInSync || dec.ConflictChange != "" {
		t.Fatalf("want a clean sync, got %+v", dec)
	}

	newA, _, _ := store.GetChange(ctx, stackIDA)
	newB, _, _ := store.GetChange(ctx, stackIDB)
	if newA.BaseSHA != newTip {
		t.Fatalf("A's base: want new trunk tip %s, got %s", newTip, newA.BaseSHA)
	}
	if newA.HeadSHA == headA || newB.HeadSHA == headB {
		t.Fatal("rebased heads must be new commits")
	}
	if newB.BaseSHA != newA.HeadSHA {
		t.Fatalf("stack broke: B's base %s is not A's new head %s", newB.BaseSHA, newA.HeadSHA)
	}
	for _, id := range []string{stackIDA, stackIDB} {
		c, _, _ := store.GetChange(ctx, id)
		ref, err := gitfixtureRunGit(bare, "rev-parse", "--verify", "refs/changes/"+id+"/head")
		if err != nil || ref != c.HeadSHA {
			t.Fatalf("%s: stable ref %q does not match stored head %q (%v)", id, ref, c.HeadSHA, err)
		}
		msg, err := gitfixtureRunGit(bare, "log", "-1", "--format=%B", c.HeadSHA)
		if err != nil || !strings.Contains(msg, "Change-Id: "+id) {
			t.Fatalf("%s: rebased head lost its Change-Id trailer: %q (%v)", id, msg, err)
		}
		author, _ := gitfixtureRunGit(bare, "log", "-1", "--format=%an", c.HeadSHA)
		if author != "Runko Test" {
			t.Fatalf("%s: rebased head lost authorship: %q", id, author)
		}
	}

	// These members declare no required checks, so their trivially-rebased
	// heads are FULLY COVERED and the sync emits no change.updated at all
	// (§13.5 carry-forward, 2026-07-15) - CI has nothing to re-run. The
	// uncovered case still emits: TestSyncPartialCoverageStillEnqueuesWebhook.
	due, _ := store.ListDueWebhookDeliveries(ctx, time.Now().Add(time.Hour))
	for _, d := range due {
		if d.EventType == "change.updated" &&
			(strings.Contains(string(d.Payload), newA.HeadSHA) || strings.Contains(string(d.Payload), newB.HeadSHA)) {
			t.Fatalf("a covered synced member must not re-trigger CI, got a change.updated for it")
		}
	}

	// The rebased commits are real: the stack lands bottom-up and trunk
	// ends up with the independent commit AND both stack files.
	for _, id := range []string{stackIDA, stackIDB} {
		c, _, _ := store.GetChange(ctx, id)
		if dec, apiErr := srv.landChangeCore(ctx, id, c, nil, nil, false); apiErr != nil || !dec.Landed {
			t.Fatalf("landing %s after sync: %+v %+v", id, dec, apiErr)
		}
	}
	tree, err := gitfixtureRunGit(bare, "ls-tree", "-r", "--name-only", "refs/heads/main")
	if err != nil {
		t.Fatalf("ls-tree trunk: %v", err)
	}
	for _, want := range []string{"other/note.txt", "proj/a.txt", "proj/b.txt"} {
		if !strings.Contains(tree, want) {
			t.Fatalf("landed trunk is missing %s:\n%s", want, tree)
		}
	}
}

func TestSyncChangeConflictReportsAndWritesNothing(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	_, repo, trunkTip, headA, headB := pushStackedPair(t, bare, store)
	ctx := context.Background()

	// Trunk moves with a CONFLICTING edit to the file change A touches.
	repo.Run("checkout --detach " + trunkTip)
	repo.WriteFile("proj/a.txt", "trunk's own conflicting a\n")
	repo.Commit("conflicting land")
	pushCommit(t, repo, bare, "refs/heads/main")
	repo.Run("checkout main")

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store}
	chB, _, _ := store.GetChange(ctx, stackIDB)
	dec, apiErr := srv.syncChangeCore(ctx, stackIDB, chB, nil)
	if apiErr != nil {
		t.Fatalf("sync: %+v", apiErr)
	}
	if dec.Synced || dec.AlreadyInSync {
		t.Fatalf("want a conflict outcome, got %+v", dec)
	}
	if dec.ConflictChange != stackIDA {
		t.Fatalf("conflicting member: want %s, got %q", stackIDA, dec.ConflictChange)
	}
	found := false
	for _, p := range dec.ConflictPaths {
		if p == "proj/a.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("conflict paths should name proj/a.txt: %v", dec.ConflictPaths)
	}

	// All-or-nothing: neither store rows nor refs moved.
	for _, want := range []struct{ id, head string }{{stackIDA, headA}, {stackIDB, headB}} {
		c, _, _ := store.GetChange(ctx, want.id)
		if c.HeadSHA != want.head {
			t.Fatalf("%s: head moved despite conflict: %s -> %s", want.id, want.head, c.HeadSHA)
		}
		ref, _ := gitfixtureRunGit(bare, "rev-parse", "--verify", "refs/changes/"+want.id+"/head")
		if ref != want.head {
			t.Fatalf("%s: ref moved despite conflict: %s -> %s", want.id, want.head, ref)
		}
	}
}

func TestSyncChangeAlreadyInSync(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	_, _, _, headA, headB := pushStackedPair(t, bare, store)
	ctx := context.Background()

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store}
	chA, _, _ := store.GetChange(ctx, stackIDA)
	dec, apiErr := srv.syncChangeCore(ctx, stackIDA, chA, nil)
	if apiErr != nil {
		t.Fatalf("sync: %+v", apiErr)
	}
	if !dec.AlreadyInSync || dec.Synced {
		t.Fatalf("trunk never moved: want already_in_sync, got %+v", dec)
	}
	a, _, _ := store.GetChange(ctx, stackIDA)
	b, _, _ := store.GetChange(ctx, stackIDB)
	if a.HeadSHA != headA || b.HeadSHA != headB {
		t.Fatal("already-in-sync must not touch heads")
	}
}

func TestSyncChangeRefusesNonOpenChange(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	pushStackedPair(t, bare, store)
	ctx := context.Background()

	if _, err := store.MarkChangeAbandoned(ctx, stackIDB); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store}
	chB, _, _ := store.GetChange(ctx, stackIDB)
	if _, apiErr := srv.syncChangeCore(ctx, stackIDB, chB, nil); apiErr == nil || apiErr.Status != http.StatusConflict {
		t.Fatalf("syncing an abandoned change: want 409, got %+v", apiErr)
	}
}

// TestSyncChangeAfterParentRebaseLands pins the recovery case the button
// exists for in dogfood practice: the stack's parent lands VIA REBASE (its
// landed SHA differs from its pushed head), leaving the child's recorded
// base pointing at a commit trunk never contained. Sync must treat the old
// parent head as the merge base and rebase the child onto the tip that
// carries the parent's landed content.
func TestSyncChangeAfterParentRebaseLands(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	ctx := context.Background()

	// Two projects, so the trunk mover (beta) doesn't intersect the
	// stack's affected set (alpha) and A's land takes the REBASE path
	// instead of stopping at requires_revalidation.
	repo := gitfixture.New(t)
	repo.WriteFile("proj/PROJECT.yaml", "schema: project/v1\nname: alpha\ntype: library\n")
	repo.WriteFile("beta/PROJECT.yaml", "schema: project/v1\nname: beta\ntype: library\n")
	trunkTip := repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	p := newTestProcessor(bare, store)
	repo.WriteFile("proj/a.txt", "a\n")
	repo.Commit("change A\n\nChange-Id: " + stackIDA)
	_, headA := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: zeroOID, NewSHA: headA, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push A rejected: %+v", res)
	}
	repo.WriteFile("proj/b.txt", "b\n")
	repo.Commit("change B\n\nChange-Id: " + stackIDB)
	_, headB := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: headA, NewSHA: headB, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push B rejected: %+v", res)
	}

	// Move trunk (inside beta) so A's land takes the rebase path, then land A.
	repo.Run("checkout --detach " + trunkTip)
	repo.WriteFile("beta/note.txt", "mover\n")
	repo.Commit("independent land")
	pushCommit(t, repo, bare, "refs/heads/main")
	repo.Run("checkout main")

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, AllowUnpolicedLand: true}
	chA, _, _ := store.GetChange(ctx, stackIDA)
	decA, apiErr := srv.landChangeCore(ctx, stackIDA, chA, nil, nil, false)
	if apiErr != nil || !decA.Landed {
		t.Fatalf("land A: %+v %+v", decA, apiErr)
	}
	landedA, _, _ := store.GetChange(ctx, stackIDA)
	if landedA.LandedSHA == headA {
		t.Fatal("fixture: A should have landed via rebase (landed SHA == pushed head)")
	}

	chB, _, _ := store.GetChange(ctx, stackIDB)
	dec, apiErr := srv.syncChangeCore(ctx, stackIDB, chB, nil)
	if apiErr != nil || !dec.Synced {
		t.Fatalf("sync child of rebase-landed parent: %+v %+v", dec, apiErr)
	}
	newB, _, _ := store.GetChange(ctx, stackIDB)
	tip, _ := gitfixtureRunGit(bare, "rev-parse", "refs/heads/main")
	if newB.BaseSHA != tip {
		t.Fatalf("B's base: want trunk tip %s, got %s", tip, newB.BaseSHA)
	}
	if decB, apiErr := srv.landChangeCore(ctx, stackIDB, newB, nil, nil, false); apiErr != nil || !decB.Landed {
		t.Fatalf("land B after sync: %+v %+v", decB, apiErr)
	}
}
