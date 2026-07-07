package main

import (
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

// newBareRemote creates a local bare repo to stand in for a real remote -
// git's local-path protocol makes this a genuine push/fetch round trip
// without any network dependency.
func newBareRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := runGit(dir, "init", "-q", "--bare", "-b", "main"); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}
	return dir
}

// configureIdentity sets persistent git identity config on repo.Dir.
// gitfixture.Repo's own git commands pass identity via per-invocation -c
// flags for reproducibility, but PushChange shells out via this package's
// plain runGit (matching what a real CLI does on a real user's machine,
// where identity is already configured globally) - tests need it set for
// real, not per-invocation, so `git commit --amend` works.
func configureIdentity(t *testing.T, dir string) {
	t.Helper()
	if _, err := runGit(dir, "config", "user.name", "Test"); err != nil {
		t.Fatalf("configure user.name: %v", err)
	}
	if _, err := runGit(dir, "config", "user.email", "test@runko.dev"); err != nil {
		t.Fatalf("configure user.email: %v", err)
	}
}

func TestPushChangeGoesToMagicRef(t *testing.T) {
	remote := newBareRemote(t)

	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	repo.Run("remote add origin " + remote)
	// Establish main on the remote first, as a real workflow would.
	if _, err := runGit(repo.Dir, "push", "origin", "main"); err != nil {
		t.Fatalf("seed remote main: %v", err)
	}

	repo.WriteFile("feature.txt", "new feature\n")
	repo.Commit("add a feature, no trailer yet")

	changeID, err := PushChange(repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("PushChange: %v", err)
	}
	if changeID == "" {
		t.Fatalf("expected a non-empty Change-Id")
	}

	// The magic ref must exist on the remote, and its commit message must
	// carry the returned Change-Id.
	msg, err := runGit(remote, "log", "-1", "--format=%B", "refs/for/main")
	if err != nil {
		t.Fatalf("expected refs/for/main to exist on the remote: %v", err)
	}
	if !strings.Contains(msg, "Change-Id: "+changeID) {
		t.Fatalf("expected remote commit message to contain Change-Id %s, got:\n%s", changeID, msg)
	}

	// refs/heads/main on the remote must be untouched - the magic ref is not
	// a direct trunk write (§7.4, §11.5).
	mainTip, err := runGit(remote, "rev-parse", "main")
	if err != nil {
		t.Fatalf("rev-parse remote main: %v", err)
	}
	forMainTip, _ := runGit(remote, "rev-parse", "refs/for/main")
	if mainTip == forMainTip {
		t.Fatalf("expected refs/for/main to differ from main (new commit), got same SHA %s", mainTip)
	}
}

func TestPushChangePreservesExistingChangeID(t *testing.T) {
	remote := newBareRemote(t)

	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	repo.Run("remote add origin " + remote)
	if _, err := runGit(repo.Dir, "push", "origin", "main"); err != nil {
		t.Fatalf("seed remote main: %v", err)
	}

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add feature\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")

	changeID, err := PushChange(repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("PushChange: %v", err)
	}
	if changeID != "I0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("expected the existing Change-Id to be preserved, got %s", changeID)
	}
}

// TestPushChangeAmendAndRepushSucceeds guards against a real bug found
// while building runkod (§28.3 stage 10): runkod keeps refs/for/<trunk> as a
// literal, repeatedly-overwritten ref rather than redirecting it server-side
// the way real Gerrit's customized receive-pack does (see PushChange's
// doc comment) - so amending a Change and re-pushing is a non-fast-forward
// update to that same ref, which a plain (non-forced) push refuses.
func TestPushChangeAmendAndRepushSucceeds(t *testing.T) {
	remote := newBareRemote(t)

	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	repo.Run("remote add origin " + remote)
	if _, err := runGit(repo.Dir, "push", "origin", "main"); err != nil {
		t.Fatalf("seed remote main: %v", err)
	}

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add feature")
	if _, err := PushChange(repo.Dir, "origin", "main"); err != nil {
		t.Fatalf("PushChange (first push): %v", err)
	}

	repo.WriteFile("feature.txt", "v2\n")
	if _, err := runGit(repo.Dir, "add", "feature.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGit(repo.Dir, "commit", "--amend", "--no-edit"); err != nil {
		t.Fatalf("amend: %v", err)
	}

	changeID, err := PushChange(repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("PushChange (amended re-push) - must not fail non-fast-forward: %v", err)
	}
	if changeID == "" {
		t.Fatalf("expected a Change-Id on the re-push")
	}

	head, err := runGit(repo.Dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	remoteTip, err := runGit(remote, "rev-parse", "refs/for/main")
	if err != nil {
		t.Fatalf("rev-parse remote refs/for/main: %v", err)
	}
	if head != remoteTip {
		t.Fatalf("expected refs/for/main to reflect the amended commit %s, got %s", head, remoteTip)
	}
}

// Note: "does re-pushing an amended commit update the same Change" is NOT
// tested here. That behavior belongs to the server's receive funnel
// (refs/for/<trunk> is a magic ref the server intercepts and maps to a
// Change/refs/changes/<id>/head by Change-Id, §11.5) - not to a literal
// fast-forward ref update, which is all a plain bare repo (no server) can
// give us as a stand-in. Against a plain remote, re-pushing an amended
// commit to the same literal ref correctly fails non-fast-forward, since
// there is no receive funnel here to special-case it. Testing that
// end-to-end requires the not-yet-built server (see receive/doc.go's scope
// note); PushChange's own responsibility - ensure a trailer, then push - is
// covered by the two tests above.
