package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/land"
)

// syncFixture: a clone with one local commit on top of trunk tip T0, and
// a remote whose trunk has since advanced to T1 - the exact state every
// sync-shaped feature exists for.
func syncFixture(t *testing.T, conflicting bool) (repo *gitfixture.Repo, remote, newTip string) {
	t.Helper()
	remote = newBareRemote(t)
	repo = gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	repo.Run("remote add origin " + remote)
	if _, err := runGit(repo.Dir, "push", "-q", "origin", "main"); err != nil {
		t.Fatalf("seed remote main: %v", err)
	}

	// Local work.
	local := "feature.txt"
	if conflicting {
		local = "shared.txt"
	}
	repo.WriteFile(local, "local line\n")
	repo.Commit("local work")

	// Trunk advances behind our back.
	other := gitfixture.New(t)
	configureIdentity(t, other.Dir)
	other.Run("remote add origin " + remote)
	if _, err := runGit(other.Dir, "fetch", "-q", "origin", "main"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, err := runGit(other.Dir, "reset", "-q", "--hard", "FETCH_HEAD"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	other.WriteFile("shared.txt", "trunk line\n")
	other.Commit("trunk advances")
	if _, err := runGit(other.Dir, "push", "-q", "origin", "main"); err != nil {
		t.Fatalf("advance remote main: %v", err)
	}
	tip, err := runGit(remote, "rev-parse", "main")
	if err != nil {
		t.Fatalf("rev-parse remote main: %v", err)
	}
	return repo, remote, tip
}

func TestSyncToTrunkRebasesOntoNewTip(t *testing.T) {
	repo, _, newTip := syncFixture(t, false)

	got, err := SyncToTrunk(repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("SyncToTrunk: %v", err)
	}
	if got != newTip {
		t.Fatalf("expected sync to report tip %s, got %s", newTip, got)
	}
	if mb, _ := runGit(repo.Dir, "merge-base", "HEAD", newTip); mb != newTip {
		t.Fatalf("expected HEAD rebased onto %s, merge-base says %s", newTip, mb)
	}

	// Second sync is a no-op, not an error.
	again, err := SyncToTrunk(repo.Dir, "origin", "main")
	if err != nil || again != newTip {
		t.Fatalf("expected idempotent no-op sync, got tip=%s err=%v", again, err)
	}
}

func TestSyncToTrunkConflictIsStructuredAndAborted(t *testing.T) {
	repo, _, _ := syncFixture(t, true)
	headBefore, _ := runGit(repo.Dir, "rev-parse", "HEAD")

	_, err := SyncToTrunk(repo.Dir, "origin", "main")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "sync_conflict" {
		t.Fatalf("expected structured sync_conflict, got %v", err)
	}
	if !strings.Contains(ce.Message, "shared.txt") {
		t.Fatalf("expected the conflicted file named, got: %s", ce.Message)
	}
	// §6.6: never a half-rebased tree - the rebase was aborted.
	if head, _ := runGit(repo.Dir, "rev-parse", "HEAD"); head != headBefore {
		t.Fatalf("expected HEAD restored to %s after abort, got %s", headBefore, head)
	}
	if status, _ := runGit(repo.Dir, "status", "--porcelain"); status != "" {
		t.Fatalf("expected a clean tree after abort, got:\n%s", status)
	}
}

func TestStaleBase(t *testing.T) {
	repo, remote, _ := syncFixture(t, false)
	if !staleBase(repo.Dir, "origin", "main") {
		t.Fatalf("trunk advanced behind us - expected staleBase true")
	}
	if _, err := SyncToTrunk(repo.Dir, "origin", "main"); err != nil {
		t.Fatalf("SyncToTrunk: %v", err)
	}
	if staleBase(repo.Dir, "origin", "main") {
		t.Fatalf("just synced - expected staleBase false")
	}
	_ = remote
}

// The sync feature's push half: a stale base is rebased onto the trunk
// tip BEFORE the magic-ref push, so the server sees a current base and
// the change never enters the §13.5 revalidation loop at all.
func TestPushChangeAutoSyncsStaleBase(t *testing.T) {
	repo, remote, newTip := syncFixture(t, false)

	if _, err := PushChange(repo.Dir, "origin", "main"); err != nil {
		t.Fatalf("PushChange: %v", err)
	}
	pushed, err := runGit(remote, "rev-parse", "refs/for/main")
	if err != nil {
		t.Fatalf("expected refs/for/main on the remote: %v", err)
	}
	if mb, _ := runGit(remote, "merge-base", pushed, newTip); mb != newTip {
		t.Fatalf("expected the pushed head based on trunk tip %s, merge-base says %s", newTip, mb)
	}
}

func TestPushChangeNoSyncPushesStaleBaseAsIs(t *testing.T) {
	repo, remote, newTip := syncFixture(t, false)

	if _, err := pushChange(repo.Dir, "origin", "main", false, false); err != nil {
		t.Fatalf("pushChange(no sync): %v", err)
	}
	pushed, _ := runGit(remote, "rev-parse", "refs/for/main")
	if mb, _ := runGit(remote, "merge-base", pushed, newTip); mb == newTip {
		t.Fatalf("--no-sync must not rebase, but the pushed head is based on the new tip")
	}
}

// A conflicting auto-sync must not block the submit (2026-07-17):
// conflicts gate LANDING (the land engine refuses a conflicting rebase
// server-side, §13.5), not review or CI. The push warns on stderr, keeps
// the stale base, and leaves the tree clean.
func TestPushChangePushesStaleBaseOnSyncConflict(t *testing.T) {
	repo, remote, newTip := syncFixture(t, true)

	var warnings strings.Builder
	oldWarn := warnWriter
	warnWriter = &warnings
	defer func() { warnWriter = oldWarn }()

	if _, err := PushChange(repo.Dir, "origin", "main"); err != nil {
		t.Fatalf("PushChange with a conflicting stale base: %v", err)
	}

	head, _ := runGit(repo.Dir, "rev-parse", "HEAD")
	pushed, err := runGit(remote, "rev-parse", "refs/for/main")
	if err != nil || pushed != head {
		t.Fatalf("expected refs/for/main = local HEAD %s, got %s (%v)", head, pushed, err)
	}
	// The conflicting rebase was NOT applied: the pushed head still sits
	// on the stale base.
	if _, err := runGit(repo.Dir, "merge-base", "--is-ancestor", newTip, head); err == nil {
		t.Fatalf("expected the push to keep the stale base, but HEAD contains the new trunk tip")
	}
	// §6.6: never a half-rebased tree.
	if status, _ := runGit(repo.Dir, "status", "--porcelain"); status != "" {
		t.Fatalf("expected a clean tree after the push, got:\n%s", status)
	}
	w := warnings.String()
	if !strings.Contains(w, "shared.txt") || !strings.Contains(w, "stale base") {
		t.Fatalf("expected a warning naming the conflicted file and the stale-base push, got: %q", w)
	}
}

// LandWithSync drives the whole §13.5 recovery loop: a first land that
// 409s requires_revalidation triggers sync + re-push + a requirements
// poll, then the retry lands. The daemon is an httptest script; the git
// side (fetch, rebase, magic-ref push) is real.
func TestLandWithSyncRecoversFromRevalidation(t *testing.T) {
	repo, remote, newTip := syncFixture(t, false)
	changeID, err := PushChange(repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("seed PushChange: %v", err)
	}

	var landCalls, reqCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/land"):
			landCalls++
			io.Copy(io.Discard, r.Body)
			if landCalls == 1 {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(&clierr.Error{
					Code: "requires_revalidation", Field: "change",
					Message: "trunk has moved in a way that intersects this change's affected set",
				})
				return
			}
			json.NewEncoder(w).Encode(land.Outcome{Landed: true, LandedSHA: "abc123"})
		case strings.HasSuffix(r.URL.Path, "/merge-requirements"):
			reqCalls++
			json.NewEncoder(w).Encode(checks.MergeRequirements{Mergeable: true})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cred := Credential{URL: srv.URL, Secret: "sekret"}
	var progress strings.Builder
	outcome, err := LandWithSync(context.Background(), http.DefaultClient, cred,
		changeID, repo.Dir, "origin", "main", time.Minute, &progress)
	if err != nil {
		t.Fatalf("LandWithSync: %v\nprogress:\n%s", err, progress.String())
	}
	if !outcome.Landed || outcome.LandedSHA != "abc123" {
		t.Fatalf("expected a landed outcome, got %+v", outcome)
	}
	if landCalls != 2 || reqCalls < 1 {
		t.Fatalf("expected land twice with a requirements poll between, got land=%d requirements=%d", landCalls, reqCalls)
	}
	// The recovery really re-pushed a rebased head.
	pushed, _ := runGit(remote, "rev-parse", "refs/for/main")
	if mb, _ := runGit(remote, "merge-base", pushed, newTip); mb != newTip {
		t.Fatalf("expected the re-pushed head based on trunk tip %s, merge-base says %s", newTip, mb)
	}
	if !strings.Contains(progress.String(), "syncing") {
		t.Fatalf("expected loop progress on the writer, got: %q", progress.String())
	}
}

// A failed required check after the sync must stop the loop with a
// structured error, not retry forever.
func TestLandWithSyncStopsOnFailedChecks(t *testing.T) {
	repo, _, _ := syncFixture(t, false)
	changeID, err := PushChange(repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("seed PushChange: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/land"):
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(&clierr.Error{Code: "requires_revalidation", Field: "change", Message: "trunk moved"})
		case strings.HasSuffix(r.URL.Path, "/merge-requirements"):
			json.NewEncoder(w).Encode(checks.MergeRequirements{FailingChecks: []string{"platform-check"}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	_, err = LandWithSync(context.Background(), http.DefaultClient, Credential{URL: srv.URL, Secret: "s"},
		changeID, repo.Dir, "origin", "main", time.Minute, io.Discard)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "checks_failed" {
		t.Fatalf("expected structured checks_failed, got %v", err)
	}
	if !strings.Contains(ce.Message, "platform-check") {
		t.Fatalf("expected the failing check named, got: %s", ce.Message)
	}
}
