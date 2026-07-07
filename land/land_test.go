package land

import (
	"sync"
	"testing"

	"github.com/saxocellphone/runko/affected"
	"github.com/saxocellphone/runko/core"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/internal/gitstore"
)

func TestLandFastForwardWhenTrunkUnchanged(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("a.txt", "orig\n")
	base := repo.Commit("base")

	repo.Run("checkout -q " + base)
	repo.WriteFile("new.txt", "hello\n")
	changeHead := repo.Commit("change")

	store := gitstore.New(repo.Dir)
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAffectedIntersection, affected.Result{}, nil, affected.Options{},
		core.CommitMeta{Message: "land"})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if !outcome.Landed || outcome.LandedSHA != changeHead {
		t.Fatalf("expected fast-forward land to changeHead, got %+v", outcome)
	}
	tip, err := store.ResolveRef("refs/heads/main")
	if err != nil || string(tip) != changeHead {
		t.Fatalf("expected refs/heads/main == changeHead, got %s (err %v)", tip, err)
	}
}

func TestLandRebasesWhenNoIntersection(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("libs/billing/lib.go", "package billing\n")
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n")
	base := repo.Commit("base")

	repo.WriteFile("libs/billing/lib.go", "package billing\n// trunk change\n")
	trunkTip := repo.Commit("trunk touches billing")

	repo.Run("checkout -q " + base)
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n// change\n")
	changeHead := repo.Commit("change touches checkout")

	projects := []affected.ProjectInfo{
		{Name: "billing-lib", Path: "libs/billing"},
		{Name: "checkout-api", Path: "commerce/checkout"},
	}

	changeAffected := affected.Result{Projects: []affected.ProjectRef{{Name: "checkout-api", Path: "commerce/checkout"}}}

	store := gitstore.New(repo.Dir)
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAffectedIntersection, changeAffected, projects, affected.Options{},
		core.CommitMeta{Message: "land"})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if outcome.RequiresRevalidation {
		t.Fatalf("did not expect revalidation, got %+v", outcome)
	}
	if !outcome.Landed || outcome.LandedSHA == "" || outcome.LandedSHA == trunkTip {
		t.Fatalf("expected land onto a new rebased commit, got %+v (old trunk tip %s)", outcome, trunkTip)
	}

	billing, err := store.GetBlob(core.Revision(outcome.LandedSHA), "libs/billing/lib.go")
	if err != nil || string(billing.Content) != "package billing\n// trunk change\n" {
		t.Fatalf("expected trunk's billing content to survive the rebase, got %q (err %v)", billing.Content, err)
	}
	checkout, err := store.GetBlob(core.Revision(outcome.LandedSHA), "commerce/checkout/handler.go")
	if err != nil || string(checkout.Content) != "package checkout\n// change\n" {
		t.Fatalf("expected the change's checkout content to survive the rebase, got %q (err %v)", checkout.Content, err)
	}
}

func TestLandRequiresRevalidationOnIntersection(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/other.go", "package checkout\n")
	base := repo.Commit("base")

	// Trunk touches the SAME project (checkout-api) the Change is scoped to.
	repo.WriteFile("commerce/checkout/other.go", "package checkout\n// trunk change\n")
	repo.Commit("trunk touches checkout-api too")

	repo.Run("checkout -q " + base)
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n// change\n")
	changeHead := repo.Commit("change touches checkout-api")

	projects := []affected.ProjectInfo{{Name: "checkout-api", Path: "commerce/checkout"}}
	changeAffected := affected.Result{Projects: []affected.ProjectRef{{Name: "checkout-api", Path: "commerce/checkout"}}}

	store := gitstore.New(repo.Dir)
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAffectedIntersection, changeAffected, projects, affected.Options{},
		core.CommitMeta{Message: "land"})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if !outcome.RequiresRevalidation {
		t.Fatalf("expected revalidation to be required, got %+v", outcome)
	}
	if outcome.Landed {
		t.Fatalf("did not expect a land when revalidation is required")
	}

	tip, err := store.ResolveRef("refs/heads/main")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if string(tip) == changeHead {
		t.Fatalf("trunk must not have advanced to changeHead when revalidation is required")
	}
}

func TestLandRevalidationAlwaysOverridesIntersection(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("libs/billing/lib.go", "package billing\n")
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n")
	base := repo.Commit("base")

	repo.WriteFile("libs/billing/lib.go", "package billing\n// trunk change\n")
	repo.Commit("trunk touches billing only")

	repo.Run("checkout -q " + base)
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n// change\n")
	changeHead := repo.Commit("change touches checkout only")

	projects := []affected.ProjectInfo{
		{Name: "billing-lib", Path: "libs/billing"},
		{Name: "checkout-api", Path: "commerce/checkout"},
	}

	changeAffected := affected.Result{Projects: []affected.ProjectRef{{Name: "checkout-api", Path: "commerce/checkout"}}}

	store := gitstore.New(repo.Dir)
	// Even though the sets don't intersect, RevalidationAlways must force it.
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAlways, changeAffected, projects, affected.Options{},
		core.CommitMeta{Message: "land"})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if !outcome.RequiresRevalidation || outcome.Landed {
		t.Fatalf("expected RevalidationAlways to force revalidation, got %+v", outcome)
	}
}

func TestLandConflict(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("a.txt", "line1\n")
	base := repo.Commit("base")

	repo.WriteFile("a.txt", "line1\ntrunk-version\n")
	repo.Commit("trunk changes a.txt")

	repo.Run("checkout -q " + base)
	repo.WriteFile("a.txt", "line1\nchange-version\n")
	changeHead := repo.Commit("change also changes a.txt")

	store := gitstore.New(repo.Dir)
	// a.txt is unowned by any registered project (there are none here), so
	// the conservative default would correctly fail closed to
	// RequiresRevalidation before ever reaching Rebase() - exactly the
	// fixed behavior this package now guarantees. Use aggressive strictness
	// deliberately to reach the rebase-conflict path in isolation; this
	// mirrors an org that has explicitly accepted that risk for
	// unregistered paths (§14.5.3), not a gap in the default.
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAffectedIntersection, affected.Result{}, nil,
		affected.Options{Strictness: affected.StrictnessAggressive},
		core.CommitMeta{Message: "land"})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if outcome.Landed {
		t.Fatalf("expected a conflicting rebase not to land")
	}
	if len(outcome.Conflicts) != 1 || outcome.Conflicts[0] != "a.txt" {
		t.Fatalf("expected a conflict on a.txt, got %+v", outcome)
	}
}

// TestLandChangeRunEverythingForcesRevalidation proves NeedsRevalidation
// honors RunEverything on the CHANGE's own affected result, not just
// project-name intersection. RunEverything means the Projects list is an
// incomplete view by construction (§13.3) - a Change whose own affected
// computation had to fail closed must never land on a "no intersection"
// technicality just because its (incomplete) project list happens not to
// overlap the trunk delta's.
func TestLandChangeRunEverythingForcesRevalidation(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("libs/billing/lib.go", "package billing\n")
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n")
	base := repo.Commit("base")

	// Trunk touches ONLY billing - no name overlap with checkout-api at all.
	repo.WriteFile("libs/billing/lib.go", "package billing\n// trunk change\n")
	repo.Commit("trunk touches billing only")

	repo.Run("checkout -q " + base)
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n// change\n")
	changeHead := repo.Commit("change touches checkout only")

	projects := []affected.ProjectInfo{
		{Name: "billing-lib", Path: "libs/billing"},
		{Name: "checkout-api", Path: "commerce/checkout"},
	}
	changeAffected := affected.Result{
		RunEverything: true, // simulates the Change's own affected computation having failed closed
		Projects:      []affected.ProjectRef{{Name: "checkout-api", Path: "commerce/checkout"}},
	}

	store := gitstore.New(repo.Dir)
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAffectedIntersection, changeAffected, projects, affected.Options{},
		core.CommitMeta{Message: "land"})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if !outcome.RequiresRevalidation || outcome.Landed {
		t.Fatalf("expected the Change's own RunEverything to force revalidation despite no name intersection, got %+v", outcome)
	}
}

// TestLandTrunkRootInvalidationForcesRevalidation and its companion below
// prove affectedOpts (root-invalidation patterns, strictness) is genuinely
// threaded through to the trunk-delta computation, not hardcoded to
// affected.Options{}. Aggressive strictness isolates this from the separate
// conservative-default "any unowned path fails closed" rule, which would
// otherwise mask whether RootInvalidationPatterns was wired through at all.
func TestLandTrunkRootInvalidationForcesRevalidation(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n")
	repo.WriteFile("go.mod", "module example\n")
	base := repo.Commit("base")

	repo.WriteFile("go.mod", "module example\nrequire foo v1.0.0\n")
	repo.Commit("trunk bumps a dependency in go.mod")

	repo.Run("checkout -q " + base)
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n// change\n")
	changeHead := repo.Commit("change touches checkout only")

	projects := []affected.ProjectInfo{{Name: "checkout-api", Path: "commerce/checkout"}}
	changeAffected := affected.Result{Projects: []affected.ProjectRef{{Name: "checkout-api", Path: "commerce/checkout"}}}

	store := gitstore.New(repo.Dir)
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAffectedIntersection, changeAffected, projects,
		affected.Options{Strictness: affected.StrictnessAggressive, RootInvalidationPatterns: []string{"go.mod"}},
		core.CommitMeta{Message: "land"})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if !outcome.RequiresRevalidation || outcome.Landed {
		t.Fatalf("expected go.mod matching a configured root-invalidation pattern to force revalidation, got %+v", outcome)
	}
}

func TestLandAggressiveModeWithoutRootPatternSkipsRevalidation(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n")
	repo.WriteFile("go.mod", "module example\n")
	base := repo.Commit("base")

	repo.WriteFile("go.mod", "module example\nrequire foo v1.0.0\n")
	repo.Commit("trunk bumps a dependency in go.mod")

	repo.Run("checkout -q " + base)
	repo.WriteFile("commerce/checkout/handler.go", "package checkout\n// change\n")
	changeHead := repo.Commit("change touches checkout only")

	projects := []affected.ProjectInfo{{Name: "checkout-api", Path: "commerce/checkout"}}
	changeAffected := affected.Result{Projects: []affected.ProjectRef{{Name: "checkout-api", Path: "commerce/checkout"}}}

	store := gitstore.New(repo.Dir)
	// Same setup as above, but no root-invalidation patterns configured and
	// aggressive strictness - the org has explicitly opted out of failing
	// closed on unowned paths, so this must land without revalidation. If
	// this test fails, affectedOpts.RootInvalidationPatterns leaked from the
	// test above or Land() is silently forcing conservative behavior.
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAffectedIntersection, changeAffected, projects,
		affected.Options{Strictness: affected.StrictnessAggressive},
		core.CommitMeta{Message: "land"})
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if outcome.RequiresRevalidation || !outcome.Landed {
		t.Fatalf("expected aggressive mode with no root patterns to land without revalidation, got %+v", outcome)
	}
}

// TestLandConcurrentRaceExactlyOneWins is the DAG's "race suite" bar
// (§28.3 stage 7, §13.5: "land races are the norm, not the edge case").
// Several concurrent Land() calls target the same trunk tip via the
// fast-forward path; git's own compare-and-swap ref update must let exactly
// one through - never a silent overwrite, never two winners.
//
// A loser can legitimately report EITHER RaceRetry or RequiresRevalidation,
// not just RaceRetry, depending on exactly when its goroutine got scheduled
// (land.go: a goroutine that reads the trunk tip before anyone has landed
// takes the fast-forward branch and loses its CAS -> RaceRetry; one that
// reads the tip AFTER the winner already moved it goes straight to the
// trunk-delta/NeedsRevalidation check instead, which this fixture's
// unowned-path fail-closed default always answers "true" -> RequiresRevalidation
// - never even attempting a CAS). Both are correct per Land()'s own
// contract; asserting only RaceRetry was the test's bug, not Land()'s: with
// GOMAXPROCS constrained (this repo's 12-core sandbox rarely staggers
// goroutine starts enough to see it, but a 2-core CI runner reliably does -
// caught by GitHub Actions' first real run of this suite), goroutines start
// staggered enough that RequiresRevalidation shows up often.
func TestLandConcurrentRaceExactlyOneWins(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("a.txt", "orig\n")
	base := repo.Commit("base")
	store := gitstore.New(repo.Dir)

	const attempts = 6
	changeHeads := make([]string, attempts)
	for i := range changeHeads {
		repo.Run("checkout -q " + base)
		repo.WriteFile("contender.txt", string(rune('a'+i)))
		changeHeads[i] = repo.Commit("contender")
	}
	repo.Run("checkout -q main") // return to the branch so refs/heads/main is the one Land() races on

	var wg sync.WaitGroup
	var mu sync.Mutex
	outcomes := make([]Outcome, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcome, err := Land(store, repo.Dir, "main", base, changeHeads[i],
				RevalidationAffectedIntersection, affected.Result{}, nil, affected.Options{},
				core.CommitMeta{Message: "land"})
			if err != nil {
				t.Errorf("Land(%d): %v", i, err)
				return
			}
			mu.Lock()
			outcomes[i] = outcome
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	landed, lost := 0, 0
	for _, o := range outcomes {
		switch {
		case o.Landed:
			landed++
		case o.RaceRetry, o.RequiresRevalidation:
			lost++
		default:
			t.Fatalf("unexpected outcome in race (a real conflict or error shape, not a legitimate loss): %+v", o)
		}
	}
	if landed != 1 {
		t.Fatalf("expected exactly 1 winner, got %d landed and %d non-winners: %+v", landed, lost, outcomes)
	}
	if lost != attempts-1 {
		t.Fatalf("expected the remaining %d attempts to report RaceRetry or RequiresRevalidation, got %d: %+v", attempts-1, lost, outcomes)
	}
}
