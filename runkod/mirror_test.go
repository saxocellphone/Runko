package runkod

// Outbound mirror tests (§18.6 M1). The mirror target is a plain local
// bare repo - deliberately: the worker speaks only the git protocol, so a
// path remote proves the provider-agnostic claim by construction (GitHub/
// GitLab/Gitea differ only in the https auth username, unit-tested in
// mirror/).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/platform/mirror"
)

func newMirrorFixture(t *testing.T) (*smFixture, *MirrorWorker, string) {
	t.Helper()
	f := newSMFixture(t) // trunk seeded + one open Change with a change ref
	target := newBareRepo(t)
	w := &MirrorWorker{
		Remote:   &mirror.Remote{RepoDir: f.bare, URL: target},
		Store:    f.store,
		TrunkRef: "main",
		// Effectively-infinite debounce: the land/push trigger sites still
		// exercise Trigger's nil-safety and scheduling, but tests drive
		// SyncOnce explicitly so assertions are deterministic.
		Debounce: time.Hour,
	}
	f.srv.Mirror = w
	return f, w, target
}

func TestMirrorSyncPushesTrunkChangesAndCursors(t *testing.T) {
	f, w, target := newMirrorFixture(t)
	ctx := context.Background()

	if err := w.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}

	localTrunk, _ := gitRevParse(f.bare, "refs/heads/main")
	mirrorTrunk, err := gitfixtureRunGit(target, "rev-parse", "refs/heads/main")
	if err != nil || mirrorTrunk != localTrunk {
		t.Fatalf("mirror trunk: want %s, got %s (%v)", localTrunk, mirrorTrunk, err)
	}
	if _, err := gitfixtureRunGit(target, "rev-parse", "refs/changes/"+smChangeID+"/head"); err != nil {
		t.Fatalf("change ref not mirrored: %v", err)
	}

	cursor, ok, _ := f.store.GetMirrorCursor(ctx, mirrorRemoteName, "refs/heads/main")
	if !ok || cursor.LastSyncedSHA != localTrunk || cursor.Frozen {
		t.Fatalf("trunk cursor: %+v ok=%v", cursor, ok)
	}

	// A land moves trunk; the next sync carries it (the Trigger sites are
	// wired in landChangeCore/commit - here we drive SyncOnce directly).
	f.greenAndApprove()
	if dec, apiErr := f.srv.landChangeCore(ctx, smChangeID, f.change(), nil, nil, false); apiErr != nil || !dec.Landed {
		t.Fatalf("land: %+v %+v", dec, apiErr)
	}
	if err := w.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce after land: %v", err)
	}
	newTrunk, _ := gitRevParse(f.bare, "refs/heads/main")
	if got, _ := gitfixtureRunGit(target, "rev-parse", "refs/heads/main"); got != newTrunk {
		t.Fatalf("mirror trunk after land: want %s, got %s", newTrunk, got)
	}
}

func TestMirrorDivergenceFreezesButNeverBlocksLanding(t *testing.T) {
	f, w, target := newMirrorFixture(t)
	ctx := context.Background()
	if err := w.SyncOnce(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// A foreign write lands on the mirror's trunk (someone pushed to the
	// mirror host directly - exactly what branch protection should stop).
	// gitfixture is deterministic, so the intruder must add real content
	// or its SHAs would EQUAL ours and no divergence would exist.
	foreign := newSMFixture(t)
	foreign.repo.WriteFile("intruder.txt", "pushed straight to the mirror\n")
	foreign.repo.Commit("foreign write")
	if _, err := gitfixtureRunGit(foreign.repo.Dir, "push", target, "+HEAD:refs/heads/main"); err != nil {
		t.Fatalf("simulate foreign write: %v", err)
	}
	foreignSHA, _ := gitfixtureRunGit(target, "rev-parse", "refs/heads/main")

	// Advance our trunk so a sync is needed, then sync: freeze, no overwrite.
	f.greenAndApprove()
	if dec, apiErr := f.srv.landChangeCore(ctx, smChangeID, f.change(), nil, nil, false); apiErr != nil || !dec.Landed {
		t.Fatalf("land with diverged mirror must still succeed: %+v %+v", dec, apiErr)
	}
	err := w.SyncOnce(ctx)
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("want divergence error, got %v", err)
	}
	cursor, _, _ := f.store.GetMirrorCursor(ctx, mirrorRemoteName, "refs/heads/main")
	if !cursor.Frozen {
		t.Fatalf("cursor must freeze on divergence: %+v", cursor)
	}
	if got, _ := gitfixtureRunGit(target, "rev-parse", "refs/heads/main"); got != foreignSHA {
		t.Fatalf("NEVER auto-overwrite a diverged mirror: mirror moved from %s to %s", foreignSHA, got)
	}

	// Frozen stays frozen across syncs; change refs still mirror (the
	// freeze is per-ref, not global).
	if err := w.SyncOnce(ctx); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("want frozen error on next sync, got %v", err)
	}
	if _, err := gitfixtureRunGit(target, "rev-parse", "refs/changes/"+smChangeID+"/head"); err != nil {
		t.Fatalf("change refs must keep mirroring while trunk is frozen: %v", err)
	}
}

func TestMirrorUnfreezeAdoptsRemoteTipThenOverwritesOnce(t *testing.T) {
	f, w, target := newMirrorFixture(t)
	ctx := context.Background()
	if err := w.SyncOnce(ctx); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	foreign := newSMFixture(t)
	foreign.repo.WriteFile("intruder.txt", "pushed straight to the mirror\n")
	foreign.repo.Commit("foreign write")
	if _, err := gitfixtureRunGit(foreign.repo.Dir, "push", target, "+HEAD:refs/heads/main"); err != nil {
		t.Fatalf("simulate foreign write: %v", err)
	}
	f.greenAndApprove()
	if dec, apiErr := f.srv.landChangeCore(ctx, smChangeID, f.change(), nil, nil, false); apiErr != nil || !dec.Landed {
		t.Fatalf("land: %+v %+v", dec, apiErr)
	}
	if err := w.SyncOnce(ctx); err == nil {
		t.Fatal("expected divergence freeze")
	}

	// The admin unfreeze semantics (§18.6.4): re-point the cursor at the
	// mirror's OBSERVED tip, so the next leased push overwrites the
	// divergence exactly once - handleMirrorUnfreeze does exactly this via
	// UpsertMirrorCursor(remote tip).
	remoteSHA, err := w.Remote.LsRemote("refs/heads/main")
	if err != nil {
		t.Fatalf("ls-remote: %v", err)
	}
	if err := f.store.UpsertMirrorCursor(ctx, mirrorRemoteName, "refs/heads/main", remoteSHA); err != nil {
		t.Fatalf("unfreeze: %v", err)
	}
	if err := w.SyncOnce(ctx); err != nil {
		t.Fatalf("post-unfreeze sync: %v", err)
	}
	localTrunk, _ := gitRevParse(f.bare, "refs/heads/main")
	if got, _ := gitfixtureRunGit(target, "rev-parse", "refs/heads/main"); got != localTrunk {
		t.Fatalf("post-unfreeze sync must restore local truth: want %s, got %s", localTrunk, got)
	}
	cursor, _, _ := f.store.GetMirrorCursor(ctx, mirrorRemoteName, "refs/heads/main")
	if cursor.Frozen || cursor.LastSyncedSHA != localTrunk {
		t.Fatalf("cursor after repair: %+v", cursor)
	}
}

func TestMirrorNeverPushesWorkspaceSnapshots(t *testing.T) {
	f, w, target := newMirrorFixture(t)
	ctx := context.Background()

	// Plant a workspace snapshot ref in the SoR repo.
	head, _ := gitRevParse(f.bare, "refs/heads/main")
	if _, err := gitfixtureRunGit(f.bare, "update-ref", "refs/workspaces/w1/head", head); err != nil {
		t.Fatalf("plant snapshot ref: %v", err)
	}
	if err := w.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if _, err := gitfixtureRunGit(target, "rev-parse", "--verify", "refs/workspaces/w1/head"); err == nil {
		t.Fatal("workspace snapshots are personal WIP and must never reach the mirror (§12.2)")
	}
}

// TestMirrorSelfHealsDanglingChangeRefs pins the fix for a live full-CI
// outage: a stable change ref pointing at a missing object (the
// pre-receive hook writes refs while objects are still in git's push
// quarantine; an aborted push discards the quarantine but not the ref)
// made the whole refs/changes/* wildcard push die with "fatal: bad
// object" - no change could reach the mirror, so no CI could fetch
// anything. The mirror now deletes such corpses loudly and pushes the
// healthy rest.
func TestMirrorSelfHealsDanglingChangeRefs(t *testing.T) {
	f, w, target := newMirrorFixture(t)
	ctx := context.Background()

	// A healthy change ref...
	head, _ := gitRevParse(f.bare, "refs/heads/main")
	if _, err := gitfixtureRunGit(f.bare, "update-ref", "refs/changes/Igood/head", head); err != nil {
		t.Fatalf("plant healthy ref: %v", err)
	}
	// ...and a dangling one: git update-ref refuses missing objects, so
	// write the loose ref file directly - exactly the on-disk state an
	// aborted quarantine leaves behind.
	danglingSHA := "beefbeefbeefbeefbeefbeefbeefbeefbeefbeef"
	refPath := filepath.Join(f.bare, "refs", "changes", "Ibad")
	if err := os.MkdirAll(refPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refPath, "head"), []byte(danglingSHA+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := w.SyncOnce(ctx); err != nil {
		t.Fatalf("SyncOnce should heal, not die: %v", err)
	}
	// The healthy ref reached the mirror; the corpse is gone locally.
	if _, err := gitfixtureRunGit(target, "rev-parse", "--verify", "refs/changes/Igood/head"); err != nil {
		t.Fatal("healthy change ref should have been mirrored")
	}
	if _, err := gitfixtureRunGit(f.bare, "rev-parse", "--verify", "refs/changes/Ibad/head"); err == nil {
		t.Fatal("the dangling ref should have been deleted")
	}
}
