package runkod

import (
	"context"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/receive"
)

// newBareRepo creates a real bare repo standing in for what runkod serves -
// Processor shells out to system git against this directory exactly like it
// would against a real served repo (§28.2 rule 4: shell out to git, never
// reimplement it).
func newBareRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := gitfixtureRunGit(dir, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatalf("init bare repo: %v", err)
	}
	return dir
}

// gitfixtureRunGit is a tiny local helper (this package has no runGit of its
// own outside Processor) so test setup can shell out the same way
// Processor's own methods do.
func gitfixtureRunGit(dir string, args ...string) (string, error) {
	p := &Processor{RepoDir: dir}
	return p.runGit(nil, args...)
}

// pushCommit pushes repo's current HEAD to bareDir at ref, returning the old
// and new SHAs at that ref exactly as a real pre-receive hook would see them
// (zeroOID if the ref didn't exist before).
func pushCommit(t *testing.T, repo *gitfixture.Repo, bareDir, ref string) (oldSHA, newSHA string) {
	t.Helper()
	oldSHA, err := gitfixtureRunGit(bareDir, "rev-parse", "--verify", "-q", ref)
	if err != nil {
		oldSHA = zeroOID
	}
	if _, err := gitfixtureRunGit(repo.Dir, "push", bareDir, "+HEAD:"+ref); err != nil {
		t.Fatalf("push HEAD to %s: %v", ref, err)
	}
	newSHA, err = gitfixtureRunGit(bareDir, "rev-parse", "--verify", ref)
	if err != nil {
		t.Fatalf("rev-parse %s after push: %v", ref, err)
	}
	return oldSHA, newSHA
}

func newTestProcessor(bareDir string, store Store) *Processor {
	return &Processor{RepoDir: bareDir, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
}

func TestProcessMagicRefPushCreatesChange(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main") // seed trunk directly on the bare repo (no funnel involved in setup)

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add a feature")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, nil)

	if !result.Accepted {
		t.Fatalf("expected the magic-ref push to be accepted, got %+v", result)
	}
	if result.ChangeID == "" {
		t.Fatalf("expected a Change-Id to be assigned")
	}
	change, ok, err := store.GetChange(context.Background(), result.ChangeID)
	if err != nil || !ok {
		t.Fatalf("expected the Change to be persisted: ok=%v err=%v", ok, err)
	}
	if change.HeadSHA != headSHA {
		t.Fatalf("expected the persisted Change's head_sha to be %s, got %s", headSHA, change.HeadSHA)
	}
}

func TestProcessDirectTrunkPushIsRejectedWithSixNineScript(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("direct push attempt")
	// Push to a scratch ref instead of really pushing to main (so gitfixture
	// doesn't move main out from under us) - we hand Process the ref update
	// exactly as a real pre-receive hook would see it for a push targeting
	// refs/heads/main.
	_, newSHA := pushCommit(t, repo, bare, "refs/heads/scratch")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: newSHA, Ref: "refs/heads/main"}, nil)

	if result.Accepted {
		t.Fatalf("expected a direct trunk push to be rejected")
	}
	if !strings.Contains(result.Message, "refs/for/main") {
		t.Fatalf("expected the §6.9 script (git push origin HEAD:refs/for/main) in the rejection, got:\n%s", result.Message)
	}
	if !strings.Contains(result.Message, "runko change push") {
		t.Fatalf("expected the CLI alternative mentioned in the rejection, got:\n%s", result.Message)
	}
	if _, ok, _ := store.GetChange(context.Background(), "anything"); ok {
		t.Fatalf("expected no Change to be persisted for a rejected push")
	}
}

func TestProcessAmendedPushUpdatesSameChange(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add feature\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, head1 := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	first := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: head1, Ref: "refs/for/main"}, nil)
	if !first.Accepted || first.ChangeID != "I0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("expected the explicit Change-Id to be used, got %+v", first)
	}

	// Amend and re-push - same Change-Id trailer, new commit content.
	repo.WriteFile("feature.txt", "v2\n")
	repo.Run("add feature.txt")
	repo.Run("commit --amend --no-edit")
	_, head2 := pushCommit(t, repo, bare, "refs/for/main")

	second := p.Process(context.Background(), RefUpdate{OldSHA: head1, NewSHA: head2, Ref: "refs/for/main"}, nil)
	if !second.Accepted || second.ChangeID != first.ChangeID {
		t.Fatalf("expected the same Change-Id to be reused across an amend, got %+v vs %+v", first, second)
	}

	change, ok, err := store.GetChange(context.Background(), first.ChangeID)
	if err != nil || !ok {
		t.Fatalf("GetChange: ok=%v err=%v", ok, err)
	}
	if change.HeadSHA != head2 {
		t.Fatalf("expected the Change's head_sha to advance to the amended commit %s, got %s", head2, change.HeadSHA)
	}
}

// TestProcessBaseSHAIsMergeBaseNotMagicRefPriorValue is a regression test
// for a real bug found while wiring land.Land into the daemon (§28.3 stage
// 11b): Change.BaseSHA used to be set from the magic ref's own prior value
// (refs/for/<trunk>'s old SHA) - zero on a Change's first push, and the
// Change's own stale prior commit on an amend. Neither is "the trunk
// commit this Change branched from". land.Land needs the real answer (git
// merge-base) to compute the trunk delta correctly; a wrong BaseSHA made
// every land look like it needed revalidation, even a trivial fast-forward.
func TestProcessBaseSHAIsMergeBaseNotMagicRefPriorValue(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	trunkTip := repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add feature\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, head1 := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	first := p.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: head1, Ref: "refs/for/main"}, nil)
	if !first.Accepted {
		t.Fatalf("expected the first push to be accepted: %+v", first)
	}
	change, _, _ := store.GetChange(context.Background(), first.ChangeID)
	if change.BaseSHA != trunkTip {
		t.Fatalf("expected BaseSHA to be the real merge-base %s, got %q", trunkTip, change.BaseSHA)
	}

	// Amend and re-push: BaseSHA must stay pinned to the same trunk commit,
	// not flip to head1 (the magic ref's prior value before this push).
	repo.WriteFile("feature.txt", "v2\n")
	repo.Run("add feature.txt")
	repo.Run("commit --amend --no-edit")
	_, head2 := pushCommit(t, repo, bare, "refs/for/main")

	second := p.Process(context.Background(), RefUpdate{OldSHA: head1, NewSHA: head2, Ref: "refs/for/main"}, nil)
	if !second.Accepted {
		t.Fatalf("expected the amended push to be accepted: %+v", second)
	}
	change2, _, _ := store.GetChange(context.Background(), first.ChangeID)
	if change2.BaseSHA != trunkTip {
		t.Fatalf("expected BaseSHA to remain %s across an amend, got %q", trunkTip, change2.BaseSHA)
	}
}

// TestProcessCreatesStablePerChangeRef guards a real gap found in review:
// refs/for/<trunk> is a single literal ref every push (any Change, any
// amend) overwrites in turn. Without a stable per-Change ref, a second,
// unrelated Change pushed after the first makes the first Change's commit
// unreachable - a GC hazard - and runko-ci has no stable path to fetch a
// specific Change by id (§14.4.4). This asserts refs/changes/<id>/head
// survives a LATER, unrelated push moving refs/for/main on to something else.
func TestProcessCreatesStablePerChangeRef(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	initialSHA := repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("a.txt", "a\n")
	repo.Commit("change A\n\nChange-Id: I0000000000000000000000000000000000000aaa")
	_, headA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	resultA := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headA, Ref: "refs/for/main"}, nil)
	if !resultA.Accepted {
		t.Fatalf("expected change A's push to be accepted, got %+v", resultA)
	}

	changeA, ok, err := store.GetChange(context.Background(), resultA.ChangeID)
	if err != nil || !ok {
		t.Fatalf("GetChange(A): ok=%v err=%v", ok, err)
	}
	wantRefA := "refs/changes/" + resultA.ChangeID + "/head"
	if changeA.GitRef != wantRefA {
		t.Fatalf("expected Change A's git_ref to be the stable per-Change ref %s, got %s", wantRefA, changeA.GitRef)
	}
	gotA, err := gitfixtureRunGit(bare, "rev-parse", "--verify", wantRefA)
	if err != nil || gotA != headA {
		t.Fatalf("expected %s to resolve to %s, got %q (err=%v)", wantRefA, headA, gotA, err)
	}

	// A second, unrelated Change (built as a sibling of "initial", not on
	// top of A) now overwrites the SAME literal refs/for/main - real git
	// requires --force for this, since refs/for/main's current tip on the
	// remote is A's commit, and B's commit isn't a descendant of it.
	repo.Run("checkout " + initialSHA)
	repo.WriteFile("b.txt", "b\n")
	repo.Commit("change B\n\nChange-Id: I0000000000000000000000000000000000000bbb")
	oldForMain, headB := pushCommit(t, repo, bare, "refs/for/main")
	if oldForMain != headA {
		t.Fatalf("expected refs/for/main's prior tip to be A's commit %s, got %s", headA, oldForMain)
	}

	resultB := p.Process(context.Background(), RefUpdate{OldSHA: oldForMain, NewSHA: headB, Ref: "refs/for/main"}, nil)
	if !resultB.Accepted {
		t.Fatalf("expected change B's push to be accepted, got %+v", resultB)
	}

	// Change A's stable ref must be untouched by B's push.
	stillA, err := gitfixtureRunGit(bare, "rev-parse", "--verify", wantRefA)
	if err != nil || stillA != headA {
		t.Fatalf("expected %s to still resolve to A's commit %s after B's push, got %q (err=%v)", wantRefA, headA, stillA, err)
	}
	wantRefB := "refs/changes/" + resultB.ChangeID + "/head"
	gotB, err := gitfixtureRunGit(bare, "rev-parse", "--verify", wantRefB)
	if err != nil || gotB != headB {
		t.Fatalf("expected %s to resolve to B's commit %s, got %q (err=%v)", wantRefB, headB, gotB, err)
	}
}

func TestProcessSecretFindingRejectsPush(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("config.py", "API_KEY = 'super-secret'\n")
	repo.Commit("oops")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	p := &Processor{RepoDir: bare, TrunkRef: "main", Store: store, Scanner: fakeScannerFindingSecret{}}
	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, nil)

	if result.Accepted {
		t.Fatalf("expected a push with a detected secret to be rejected")
	}
	if !strings.Contains(result.Message, "config.py") {
		t.Fatalf("expected the offending path in the rejection, got:\n%s", result.Message)
	}
}

type fakeScannerFindingSecret struct{}

func (fakeScannerFindingSecret) Scan(files []receive.FileContent) ([]receive.SecretFinding, error) {
	for _, f := range files {
		if f.Path == "config.py" {
			return []receive.SecretFinding{{Path: f.Path, Line: 1, RuleID: "generic-api-key", Description: "possible API key"}}, nil
		}
	}
	return nil, nil
}

func TestProcessNonFunnelRefIsAcceptedUnconditionally(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	oldSHA, headSHA := pushCommit(t, repo, bare, "refs/workspaces/ws-1/head")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/workspaces/ws-1/head"}, nil)

	if !result.Accepted {
		t.Fatalf("expected a non-funnel ref (workspace snapshot) to be accepted unconditionally, got %+v", result)
	}
	if result.ChangeID != "" {
		t.Fatalf("expected no Change to be created for a non-funnel ref, got %+v", result)
	}
}

func TestProcessBatchRejectsWholePushIfAnyRefFails(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("good change\n\nChange-Id: I0000000000000000000000000000000000000001")
	_, goodSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	results := p.ProcessBatch(context.Background(), []RefUpdate{
		{OldSHA: oldSHA, NewSHA: goodSHA, Ref: "refs/for/main"},   // would be accepted alone
		{OldSHA: oldSHA, NewSHA: goodSHA, Ref: "refs/heads/main"}, // direct trunk push - rejected
	}, nil)

	for _, r := range results {
		if r.Accepted {
			t.Fatalf("expected the whole batch to be rejected because one ref failed, got %+v", results)
		}
	}
	if _, ok, _ := store.GetChange(context.Background(), "I0000000000000000000000000000000000000001"); ok {
		t.Fatalf("expected no Change to be persisted when the batch as a whole is rejected")
	}
}
