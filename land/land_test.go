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
	outcome, err := Land(store, repo.Dir, "main", base, changeHead, RevalidationAffectedIntersection, nil, nil, core.CommitMeta{Message: "land"})
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

	store := gitstore.New(repo.Dir)
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAffectedIntersection, []string{"checkout-api"}, projects,
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

	store := gitstore.New(repo.Dir)
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAffectedIntersection, []string{"checkout-api"}, projects,
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

	store := gitstore.New(repo.Dir)
	// Even though the sets don't intersect, RevalidationAlways must force it.
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAlways, []string{"checkout-api"}, projects,
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
	outcome, err := Land(store, repo.Dir, "main", base, changeHead,
		RevalidationAffectedIntersection, nil, nil, core.CommitMeta{Message: "land"})
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

// TestLandConcurrentRaceExactlyOneWins is the DAG's "race suite" bar
// (§28.3 stage 7, §13.5: "land races are the norm, not the edge case").
// Several concurrent Land() calls target the same trunk tip via the
// fast-forward path; git's own compare-and-swap ref update must let exactly
// one through and report RaceRetry for the rest - never a silent overwrite,
// never two winners.
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
				RevalidationAffectedIntersection, nil, nil, core.CommitMeta{Message: "land"})
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

	landed, raced := 0, 0
	for _, o := range outcomes {
		switch {
		case o.Landed:
			landed++
		case o.RaceRetry:
			raced++
		default:
			t.Fatalf("unexpected outcome in race: %+v", o)
		}
	}
	if landed != 1 {
		t.Fatalf("expected exactly 1 winner, got %d landed and %d race-retries: %+v", landed, raced, outcomes)
	}
	if raced != attempts-1 {
		t.Fatalf("expected the remaining %d attempts to report RaceRetry, got %d", attempts-1, raced)
	}
}
