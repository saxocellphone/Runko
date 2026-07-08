package runkod

// Stacked-Change regression tests (P0 from the 2026-07-08 dogfood review):
// the receive path recorded base_sha = merge-base(head, trunk) for EVERY
// magic-ref push, so a stacked Change (B's parent commit is pending Change
// A's head, §7.4) got trunk as its base - GetChangeStack saw siblings
// instead of A→B, GetChangeDiff(B) spanned the whole stack, and the only
// test covering stacks hand-rewrote the Store to the base production never
// recorded. These tests drive real sequential pushes and assert what
// production actually persists.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

const (
	stackIDA = "Iaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	stackIDB = "Ibbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// pushStackedPair seeds trunk and pushes Change A then Change B (B's commit
// parented on A's) through the real Processor, returning the fixture repo
// still parked on B's head.
func pushStackedPair(t *testing.T, bare string, store Store) (p *Processor, repo *gitfixture.Repo, trunkTip, headA, headB string) {
	t.Helper()
	repo = gitfixture.New(t)
	repo.WriteFile("proj/PROJECT.yaml", "schema: project/v1\nname: alpha\ntype: library\n")
	trunkTip = repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	p = newTestProcessor(bare, store)
	ctx := context.Background()

	repo.WriteFile("proj/a.txt", "a\n")
	repo.Commit("change A\n\nChange-Id: " + stackIDA)
	_, headA = pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: zeroOID, NewSHA: headA, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push A rejected: %+v", res)
	}

	repo.WriteFile("proj/b.txt", "b\n")
	repo.Commit("change B\n\nChange-Id: " + stackIDB)
	_, headB = pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: headA, NewSHA: headB, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push B rejected: %+v", res)
	}
	return p, repo, trunkTip, headA, headB
}

func TestStackedPushRecordsParentHeadAsBase(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	p, repo, trunkTip, headA, _ := pushStackedPair(t, bare, store)
	ctx := context.Background()

	chA, _, _ := store.GetChange(ctx, stackIDA)
	if chA.BaseSHA != trunkTip {
		t.Fatalf("A's base: want trunk tip %s, got %s", trunkTip, chA.BaseSHA)
	}
	chB, _, _ := store.GetChange(ctx, stackIDB)
	if chB.BaseSHA != headA {
		t.Fatalf("B's base: want A's head %s, got %s", headA, chB.BaseSHA)
	}

	// Amending B keeps it stacked: the amended commit's parent is still
	// A's head, and so must the recorded base be.
	repo.WriteFile("proj/b.txt", "b2\n")
	repo.Run("add proj/b.txt")
	repo.Run("commit --amend --no-edit")
	oldB := chB.HeadSHA
	_, headB2 := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: oldB, NewSHA: headB2, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("amend of B rejected: %+v", res)
	}
	chB, _, _ = store.GetChange(ctx, stackIDB)
	if chB.BaseSHA != headA || chB.HeadSHA != headB2 {
		t.Fatalf("after amend: want base %s head %s, got base %s head %s", headA, headB2, chB.BaseSHA, chB.HeadSHA)
	}
}

// TestGrownChangeKeepsItsBaseBelowTheWholeChange pins the self-Id skip in
// nearestPendingChangeBase: a commit stacked on the SAME Change-Id (the
// "grow a Change by another commit" workflow) is one Change spanning both
// commits, so its base must stay the stack parent below the whole Change -
// splitting it at its own prior commit would shrink the Change's diff to
// the top commit only.
func TestGrownChangeKeepsItsBaseBelowTheWholeChange(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	p, repo, _, headA, headB := pushStackedPair(t, bare, store)
	ctx := context.Background()

	repo.WriteFile("proj/b_more.txt", "more\n")
	repo.Commit("change B, grown\n\nChange-Id: " + stackIDB)
	_, headB2 := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: headB, NewSHA: headB2, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("grow push rejected: %+v", res)
	}
	chB, _, _ := store.GetChange(ctx, stackIDB)
	if chB.BaseSHA != headA {
		t.Fatalf("grown B's base: want A's head %s (below the whole Change), got %s", headA, chB.BaseSHA)
	}
}

// TestStackedChildCannotLandBeforeParent pins the ordering guard the real
// stack bases make necessary: attemptLand rebases only base..head onto
// trunk, so landing B while A is still pending would put B's delta on trunk
// WITHOUT the parent content it was built on. The child must 409 until the
// parent lands, then land cleanly.
func TestStackedChildCannotLandBeforeParent(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	_, _, _, _, _ = pushStackedPair(t, bare, store)
	ctx := context.Background()

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, AllowUnpolicedLand: true}

	chB, _, _ := store.GetChange(ctx, stackIDB)
	if _, apiErr := srv.landChangeCore(ctx, stackIDB, chB, nil, nil); apiErr == nil {
		t.Fatal("landing the child before the parent must be refused")
	} else {
		if apiErr.Status != http.StatusConflict || apiErr.Err.Code != "parent_change_not_landed" {
			t.Fatalf("want 409 parent_change_not_landed, got %d %q", apiErr.Status, apiErr.Err.Code)
		}
		if !strings.Contains(apiErr.Err.Suggestion, stackIDA) {
			t.Fatalf("suggestion should name the parent change: %q", apiErr.Err.Suggestion)
		}
	}

	chA, _, _ := store.GetChange(ctx, stackIDA)
	if dec, apiErr := srv.landChangeCore(ctx, stackIDA, chA, nil, nil); apiErr != nil || !dec.Landed {
		t.Fatalf("landing the parent: %+v, %+v", dec, apiErr)
	}

	chB, _, _ = store.GetChange(ctx, stackIDB)
	if dec, apiErr := srv.landChangeCore(ctx, stackIDB, chB, nil, nil); apiErr != nil || !dec.Landed {
		t.Fatalf("landing the child after the parent: %+v, %+v", dec, apiErr)
	}
}

// TestStackedPushWithUnknownIntermediateStaysConservative pins the
// keep-walking rule: an ancestor with no (or an unknown) Change-Id is part
// of THIS push's delta, so the base must stay below it - otherwise landing
// would silently drop the intermediate commit's content.
func TestStackedPushWithUnknownIntermediateStaysConservative(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("proj/PROJECT.yaml", "schema: project/v1\nname: alpha\ntype: library\n")
	trunkTip := repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	ctx := context.Background()

	// Two commits pushed in one go: the intermediate has no Change row.
	repo.WriteFile("proj/mid.txt", "mid\n")
	repo.Commit("intermediate, no change id")
	repo.WriteFile("proj/top.txt", "top\n")
	repo.Commit("the change\n\nChange-Id: " + stackIDB)
	_, head := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: zeroOID, NewSHA: head, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push rejected: %+v", res)
	}
	ch, _, _ := store.GetChange(ctx, stackIDB)
	if ch.BaseSHA != trunkTip {
		t.Fatalf("base with unknown intermediate: want trunk tip %s, got %s", trunkTip, ch.BaseSHA)
	}
}

// TestStackDerivationIgnoresLandedParents pins the residual mega-stack
// generator the receive-path fix alone doesn't kill: after a fast-forward
// land, the landed Change's head IS the trunk tip, so every independent
// Change branched from that tip has base == its head - deriving relations
// over landed Changes chains all of them into one false sibling blob.
// Stacks are pending work: only open Changes may parent relations.
func TestStackDerivationIgnoresLandedParents(t *testing.T) {
	landed := Change{ChangeKey: "Ilanded", State: "landed", BaseSHA: "t0", HeadSHA: "t1"}
	c1 := Change{ChangeKey: "Ic1", State: "open", BaseSHA: "t1", HeadSHA: "c1"}
	c2 := Change{ChangeKey: "Ic2", State: "open", BaseSHA: "t1", HeadSHA: "c2"}
	all := []Change{landed, c1, c2}

	chain, pos := stackForChange(all, c1)
	if len(chain) != 1 || chain[0].ChangeKey != "Ic1" || pos != 0 {
		t.Fatalf("independent change based at a landed head must be a single-change stack, got %+v @%d", chain, pos)
	}

	// A real open stack still derives: child of c1 chains, unrelated c2 stays out.
	child := Change{ChangeKey: "Ichild", State: "open", BaseSHA: "c1", HeadSHA: "c3"}
	chain, pos = stackForChange(append(all, child), child)
	if len(chain) != 2 || chain[0].ChangeKey != "Ic1" || chain[1].ChangeKey != "Ichild" || pos != 1 {
		t.Fatalf("open stack: want [Ic1 Ichild]@1, got %+v @%d", chain, pos)
	}
}

const stackIDC = "Icdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd"

// TestSeriesPushCreatesEveryChangeInTheStack pins Gerrit-style series
// receive (§7.4, decided 2026-07-08 with the jj-first client direction):
// ONE push of a 3-commit stack creates all three Changes, bases chained
// bottom-up.
func TestSeriesPushCreatesEveryChangeInTheStack(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("proj/PROJECT.yaml", "schema: project/v1\nname: alpha\ntype: library\n")
	trunkTip := repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	ctx := context.Background()

	repo.WriteFile("proj/a.txt", "a\n")
	headA := repo.Commit("change A\n\nChange-Id: " + stackIDA)
	repo.WriteFile("proj/b.txt", "b\n")
	headB := repo.Commit("change B\n\nChange-Id: " + stackIDB)
	repo.WriteFile("proj/c.txt", "c\n")
	repo.Commit("change C\n\nChange-Id: " + stackIDC)
	_, headC := pushCommit(t, repo, bare, "refs/for/main")

	res := p.Process(ctx, RefUpdate{OldSHA: zeroOID, NewSHA: headC, Ref: "refs/for/main"}, nil)
	if !res.Accepted || res.ChangeID != stackIDC {
		t.Fatalf("series push: %+v", res)
	}

	for _, want := range []struct{ id, head, base string }{
		{stackIDA, headA, trunkTip},
		{stackIDB, headB, headA},
		{stackIDC, headC, headB},
	} {
		ch, ok, _ := store.GetChange(ctx, want.id)
		if !ok {
			t.Fatalf("series member %s has no Change row", want.id)
		}
		if ch.HeadSHA != want.head || ch.BaseSHA != want.base {
			t.Fatalf("%s: want head %s base %s, got head %s base %s", want.id, want.head, want.base, ch.HeadSHA, ch.BaseSHA)
		}
		if _, err := gitfixtureRunGit(bare, "rev-parse", "--verify", "refs/changes/"+want.id+"/head"); err != nil {
			t.Fatalf("%s: no stable per-change ref: %v", want.id, err)
		}
	}
}

// TestSeriesRepushAfterRootAmendRestacksEveryChange is THE evolve workflow
// (jj's auto-rebase + one push; plain git's rebase + one push): amend the
// ROOT of a pushed 3-stack, rebase descendants locally, push the tip once
// - every member's head and base must move together, and the derived stack
// must stay A -> B -> C throughout.
func TestSeriesRepushAfterRootAmendRestacksEveryChange(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("proj/PROJECT.yaml", "schema: project/v1\nname: alpha\ntype: library\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	ctx := context.Background()

	repo.WriteFile("proj/a.txt", "a v1\n")
	repo.Commit("change A\n\nChange-Id: " + stackIDA)
	repo.WriteFile("proj/b.txt", "b\n")
	repo.Commit("change B\n\nChange-Id: " + stackIDB)
	repo.WriteFile("proj/c.txt", "c\n")
	repo.Commit("change C\n\nChange-Id: " + stackIDC)
	_, tip1 := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: zeroOID, NewSHA: tip1, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("initial series push: %+v", res)
	}
	oldA, _, _ := store.GetChange(ctx, stackIDA)

	// Amend the root and restack descendants - what jj does implicitly on
	// `jj edit`+change, and what plain git does with one interactive
	// rebase. GIT_SEQUENCE_EDITOR rewrites the todo to edit the root.
	if _, err := gitfixtureRunGit(repo.Dir, "-c", "user.name=Test", "-c", "user.email=test@runko.dev",
		"-c", "sequence.editor=sed -i 1s/pick/edit/", "rebase", "-i", "HEAD~3"); err != nil {
		t.Fatalf("start rebase at root: %v", err)
	}
	repo.WriteFile("proj/a.txt", "a v2 - edited at the root\n")
	for _, args := range [][]string{
		{"add", "proj/a.txt"},
		{"-c", "user.name=Test", "-c", "user.email=test@runko.dev", "commit", "--amend", "--no-edit"},
		{"-c", "user.name=Test", "-c", "user.email=test@runko.dev", "rebase", "--continue"},
	} {
		if _, err := gitfixtureRunGit(repo.Dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	_, tip2 := pushCommit(t, repo, bare, "refs/for/main")
	if tip2 == tip1 {
		t.Fatal("rebase should have rewritten the tip")
	}
	if res := p.Process(ctx, RefUpdate{OldSHA: tip1, NewSHA: tip2, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("restack push: %+v", res)
	}

	newA, _, _ := store.GetChange(ctx, stackIDA)
	newB, _, _ := store.GetChange(ctx, stackIDB)
	newC, _, _ := store.GetChange(ctx, stackIDC)
	if newA.HeadSHA == oldA.HeadSHA {
		t.Fatal("root Change's head did not move with the amend")
	}
	if newB.BaseSHA != newA.HeadSHA || newC.BaseSHA != newB.HeadSHA || newC.HeadSHA != tip2 {
		t.Fatalf("stack not re-chained: A.head=%s B.base=%s B.head=%s C.base=%s C.head=%s tip=%s",
			newA.HeadSHA, newB.BaseSHA, newB.HeadSHA, newC.BaseSHA, newC.HeadSHA, tip2)
	}

	// The derived stack view holds through the whole cycle.
	chain, pos := stackForChange([]Change{newA, newB, newC}, newB)
	if len(chain) != 3 || chain[0].ChangeKey != stackIDA || chain[2].ChangeKey != stackIDC || pos != 1 {
		t.Fatalf("derived stack after restack: %+v @%d", chain, pos)
	}
}

// TestSeriesPushSkipsLandedMembers: pushing a stack whose lower member
// already landed must not resurrect or mutate the landed row (landed is
// terminal) - the still-open upper member updates, the landed one is
// untouched history context.
func TestSeriesPushSkipsLandedMembers(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	p, repo, _, _, _ := pushStackedPair(t, bare, store)
	ctx := context.Background()

	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, AllowUnpolicedLand: true}
	chA, _, _ := store.GetChange(ctx, stackIDA)
	if dec, apiErr := srv.landChangeCore(ctx, stackIDA, chA, nil, nil); apiErr != nil || !dec.Landed {
		t.Fatalf("land A: %+v %+v", dec, apiErr)
	}
	landedA, _, _ := store.GetChange(ctx, stackIDA)

	// Amend B (still parented on A's pre-land commit) and push: the series
	// walk sees A's commit, finds its Change landed, and skips it.
	repo.WriteFile("proj/b.txt", "b amended after A landed\n")
	repo.Run("add proj/b.txt")
	repo.Run("commit --amend --no-edit")
	chB, _, _ := store.GetChange(ctx, stackIDB)
	_, newB := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: chB.HeadSHA, NewSHA: newB, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push B after A landed: %+v", res)
	}

	afterA, _, _ := store.GetChange(ctx, stackIDA)
	if afterA.State != "landed" || afterA.HeadSHA != landedA.HeadSHA {
		t.Fatalf("landed member mutated by series push: %+v -> %+v", landedA, afterA)
	}
	afterB, _, _ := store.GetChange(ctx, stackIDB)
	if afterB.HeadSHA != newB {
		t.Fatalf("open member should update: %+v", afterB)
	}
}

// TestStackedBaseOnUnbornTrunk pins the 2026-07-08 clean-slate finding: on
// a fresh monorepo (trunk unborn - the ONLY state a §6.9-closed trunk can
// bootstrap from), `^refs/heads/<trunk>` is a hard git error, and the base
// walk used to abort - every pre-first-land Change got base "", stacks
// never derived, diffs/affected spanned the whole scaffold, and land
// ordering never fired.
func TestStackedBaseOnUnbornTrunk(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	p := newTestProcessor(bare, store)
	ctx := context.Background()

	repo := gitfixture.New(t)
	repo.WriteFile("proj/PROJECT.yaml", "schema: project/v1\nname: alpha\ntype: library\n")
	repo.Commit("change A - first ever\n\nChange-Id: " + stackIDA)
	_, headA := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: zeroOID, NewSHA: headA, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push A on unborn trunk: %+v", res)
	}
	repo.WriteFile("proj/b.txt", "feature B\n")
	repo.Commit("change B\n\nChange-Id: " + stackIDB)
	_, headB := pushCommit(t, repo, bare, "refs/for/main")
	if res := p.Process(ctx, RefUpdate{OldSHA: headA, NewSHA: headB, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("push B on unborn trunk: %+v", res)
	}

	chA, _, _ := store.GetChange(ctx, stackIDA)
	chB, _, _ := store.GetChange(ctx, stackIDB)
	if chA.BaseSHA != "" {
		t.Fatalf("A is the repo's first change: want base \"\", got %q", chA.BaseSHA)
	}
	if chB.BaseSHA != headA {
		t.Fatalf("B's base on unborn trunk: want A's head %s, got %q", headA, chB.BaseSHA)
	}

	chain, _ := stackForChange([]Change{chA, chB}, chB)
	if len(chain) != 2 || chain[0].ChangeKey != stackIDA {
		t.Fatalf("stack derivation on unborn trunk: %+v", chain)
	}

	// Land ordering fires pre-first-land too: B refuses until A lands.
	srv := &Server{RepoDir: bare, TrunkRef: "main", Store: store, AllowUnpolicedLand: true}
	if _, apiErr := srv.landChangeCore(ctx, stackIDB, chB, nil, nil); apiErr == nil || apiErr.Err.Code != "parent_change_not_landed" {
		t.Fatalf("landing B before A on unborn trunk: want parent_change_not_landed, got %+v", apiErr)
	}
	if dec, apiErr := srv.landChangeCore(ctx, stackIDA, chA, nil, nil); apiErr != nil || !dec.Landed {
		t.Fatalf("bootstrap land of A: %+v %+v", dec, apiErr)
	}
	chB, _, _ = store.GetChange(ctx, stackIDB)
	if dec, apiErr := srv.landChangeCore(ctx, stackIDB, chB, nil, nil); apiErr != nil || !dec.Landed {
		t.Fatalf("landing B after A: %+v %+v", dec, apiErr)
	}
}

// TestSeriesPushOnUnbornTrunk: one push carrying the repo's first-ever
// stack - both Changes recorded, bases chained from "" upward.
func TestSeriesPushOnUnbornTrunk(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	p := newTestProcessor(bare, store)
	ctx := context.Background()

	repo := gitfixture.New(t)
	repo.WriteFile("proj/PROJECT.yaml", "schema: project/v1\nname: alpha\ntype: library\n")
	headA := repo.Commit("change A\n\nChange-Id: " + stackIDA)
	repo.WriteFile("proj/b.txt", "b\n")
	repo.Commit("change B\n\nChange-Id: " + stackIDB)
	_, headB := pushCommit(t, repo, bare, "refs/for/main")

	if res := p.Process(ctx, RefUpdate{OldSHA: zeroOID, NewSHA: headB, Ref: "refs/for/main"}, nil); !res.Accepted {
		t.Fatalf("series push on unborn trunk: %+v", res)
	}
	chA, okA, _ := store.GetChange(ctx, stackIDA)
	chB, okB, _ := store.GetChange(ctx, stackIDB)
	if !okA || !okB {
		t.Fatalf("series members missing: A=%v B=%v", okA, okB)
	}
	if chA.BaseSHA != "" || chA.HeadSHA != headA || chB.BaseSHA != headA || chB.HeadSHA != headB {
		t.Fatalf("unborn-trunk series chain: A(base=%q head=%s) B(base=%q head=%s)", chA.BaseSHA, chA.HeadSHA, chB.BaseSHA, chB.HeadSHA)
	}
}

// TestClientPushToChangeRefsNamespaceIsRejected pins the server-owned
// namespace (§14.4.4): refs/changes/<id>/head is written by the daemon on
// every accepted push, and the Store's head_sha is keyed to it - a client
// writing it directly desynchronizes git from the Store. §14.10.3's
// "everything else is accepted" permissiveness must not cover it.
func TestClientPushToChangeRefsNamespaceIsRejected(t *testing.T) {
	bare := newBareRepo(t)
	store := NewMemStore()
	p, _, _, headA, _ := pushStackedPair(t, bare, store)
	ctx := context.Background()

	res := p.Process(ctx, RefUpdate{OldSHA: zeroOID, NewSHA: headA, Ref: "refs/changes/" + stackIDA + "/head"}, nil)
	if res.Accepted {
		t.Fatal("client push to refs/changes/* must be rejected")
	}
	if !strings.Contains(res.Message, "server-owned") || !strings.Contains(res.Message, "refs/for/main") {
		t.Fatalf("rejection should explain the namespace and the right path: %q", res.Message)
	}

	// Tags stay §14.10.3-permissive - the guard is scoped, not a general
	// tightening.
	tag := p.Process(ctx, RefUpdate{OldSHA: zeroOID, NewSHA: headA, Ref: "refs/tags/v1"}, nil)
	if !tag.Accepted {
		t.Fatalf("tag pushes must stay accepted: %+v", tag)
	}
}
