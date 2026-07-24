package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
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

func TestCommitBodyDescription(t *testing.T) {
	for _, tc := range []struct {
		name, msg, want string
	}{
		{
			name: "body with trailers stripped",
			msg:  "subject line\n\nwhy this change exists.\n\nand a second paragraph.\n\nChange-Id: I123\nSigned-off-by: A <a@b.c>\n",
			want: "why this change exists.\n\nand a second paragraph.",
		},
		{
			// A one-line commit has nothing to say beyond its title, and the
			// Change already carries the title - seeding it would just
			// duplicate, and would mask the missing description.
			name: "subject only yields nothing",
			msg:  "subject line\n\nChange-Id: I123\n",
			want: "",
		},
		{name: "no body at all", msg: "subject line\n", want: ""},
		{
			// Only the LAST paragraph is trailer-eligible: prose that happens
			// to start with "Note:" must survive.
			name: "prose that looks trailer-ish is kept",
			msg:  "subject\n\nNote: this sentence is prose, not a trailer.\n\nreal body.\n\nChange-Id: I1\n",
			want: "Note: this sentence is prose, not a trailer.\n\nreal body.",
		},
		{name: "crlf", msg: "subject\r\n\r\nbody here.\r\n\r\nChange-Id: I1\r\n", want: "body here."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commitBodyDescription(tc.msg); got != tc.want {
				t.Fatalf("commitBodyDescription:\n got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// Writing the what-and-why twice - once in the commit, once for the
// control plane - is duplication, and forgetting the second surfaces much
// later as an unmergeable agent Change. push seeds it; what it must NEVER
// do is clobber a description someone set deliberately.
func TestSeedPushedDescriptionsFillsOnlyEmptyOnes(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("base")
	repo.Run("update-ref refs/remotes/origin/main HEAD")
	empty, described := fakeChangeID("seed-empty"), fakeChangeID("seed-described")
	repo.WriteFile("a.txt", "a\n")
	repo.Commit("bottom subject\n\nthe bottom body.\n\nChange-Id: " + empty)
	repo.WriteFile("b.txt", "b\n")
	repo.Commit("top subject\n\nthe top body.\n\nChange-Id: " + described)

	var posted map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, empty):
			json.NewEncoder(w).Encode(ChangeInfo{State: "open", Description: ""})
		case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, described):
			json.NewEncoder(w).Encode(ChangeInfo{State: "open", Description: "set by hand"})
		case req.Method == http.MethodPost && strings.Contains(req.URL.Path, "/describe"):
			var body map[string]string
			json.NewDecoder(req.Body).Decode(&body)
			if posted != nil {
				t.Errorf("described more than one change; second was %s", req.URL.Path)
			}
			posted = body
			if !strings.Contains(req.URL.Path, empty) {
				t.Errorf("described the wrong change: %s", req.URL.Path)
			}
			json.NewEncoder(w).Encode(ChangeInfo{State: "open"})
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()

	cred := Credential{URL: srv.URL, Secret: "tok"}
	seedPushedDescriptions(context.Background(), srv.Client(), cred, repo.Dir, "origin", "main")

	if posted == nil {
		t.Fatalf("the change with no description must be seeded from its commit body")
	}
	if posted["description"] != "the bottom body." {
		t.Fatalf("seeded the wrong text: %q", posted["description"])
	}
	if _, ok := posted["test_plan"]; ok {
		t.Fatalf("push must not invent a test plan - it has no source for one: %+v", posted)
	}
}

// The push has already succeeded by the time seeding runs, so nothing here
// may turn a good push into a failure.
func TestSeedPushedDescriptionsSurvivesAnUnreachableServer(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("base")
	repo.Run("update-ref refs/remotes/origin/main HEAD")
	repo.WriteFile("a.txt", "a\n")
	repo.Commit("subject\n\nbody.\n\nChange-Id: " + fakeChangeID("seed-unreachable"))

	cred := Credential{URL: "http://127.0.0.1:1", Secret: "tok"}
	seedPushedDescriptions(context.Background(), http.DefaultClient, cred, repo.Dir, "origin", "main")
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

	id := fakeChangeID("push-preserves-existing")
	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add feature\n\nChange-Id: " + id)

	changeID, err := PushChange(repo.Dir, "origin", "main")
	if err != nil {
		t.Fatalf("PushChange: %v", err)
	}
	if changeID != id {
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

// TestPushChangeOnNonRepoDirReturnsStructuredError and
// TestPushChangeOnEmptyRepoReturnsStructuredError close raw-passthrough gaps
// found in the CLI robustness audit (§6.5): PushChange used to surface git's
// raw exit-128 text for "not a repo" and "no commits yet", the same class of
// bug stage 9a already fixed for `project create` and `doctor`.
func TestPushChangeOnNonRepoDirReturnsStructuredError(t *testing.T) {
	dir := t.TempDir() // not a git repo at all

	_, err := PushChange(dir, "origin", "main")
	if err == nil {
		t.Fatalf("expected an error for a non-repo directory")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Code != "not_a_repo" {
		t.Fatalf("expected code not_a_repo, got %+v", ce)
	}
}

func TestPushChangeOnEmptyRepoReturnsStructuredError(t *testing.T) {
	repo := gitfixture.New(t) // git init'd, zero commits

	_, err := PushChange(repo.Dir, "origin", "main")
	if err == nil {
		t.Fatalf("expected an error when HEAD has no commits yet")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Code != "no_commits" {
		t.Fatalf("expected code no_commits, got %+v", ce)
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

// TestCreateChangeIDsAreGloballyUnique pins the 2026-07-08 dogfood finding:
// the seed was HEAD + staged path NAMES only, so two clones at the same
// trunk tip touching the same file - different content, different messages
// - minted the SAME Change-Id and fought over one Change identity on push.
// Byte-identical repos and messages are the worst case: the random nonce in
// the seed must still keep the ids distinct.
func TestCreateChangeIDsAreGloballyUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		repo := gitfixture.New(t)
		configureIdentity(t, repo.Dir)
		repo.WriteFile("README.md", "hi\n")
		repo.Commit("initial")
		repo.WriteFile("main.go", "package main\n")

		id, err := CreateChange(repo.Dir, "identical message", false)
		if err != nil {
			t.Fatalf("CreateChange %d: %v", i, err)
		}
		if seen[id] {
			t.Fatalf("clone %d minted the same Change-Id %s as another identical clone", i, id)
		}
		seen[id] = true
	}
}

// TestPushChangeRefusesWhenHeadAlreadyOnTrunk pins the 2026-07-08 dogfood
// footgun: trunk commits keep their landed Change-Id trailer, so a `runko
// change push` from a clean trunk tip - no new commit - used to re-push
// the landed commit (the daemon now also rejects it at receive; the CLI
// should never send it at all).
func TestPushChangeRefusesWhenHeadAlreadyOnTrunk(t *testing.T) {
	remote := newBareRemote(t)
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial\n\nChange-Id: " + fakeChangeID("already-on-trunk"))
	if _, err := runGit(repo.Dir, "push", remote, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("seed trunk: %v", err)
	}

	_, err := PushChange(repo.Dir, remote, "main")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "already_on_trunk" {
		t.Fatalf("want already_on_trunk, got %v", err)
	}

	// A real new commit pushes fine.
	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add feature")
	if _, err := PushChange(repo.Dir, remote, "main"); err != nil {
		t.Fatalf("push with a new commit: %v", err)
	}
}

// TestPushChangeWarnsWhenLocalConfigShadowedByWorktree pins the config-
// split trap: `git config runko.workspace x` (no --worktree) writes a
// value that LOOKS set but never wins over the worktree config `runko
// workspace attach` writes - the push must say which value it actually
// used.
func TestPushChangeWarnsWhenLocalConfigShadowedByWorktree(t *testing.T) {
	remote := newBareRemote(t)
	if _, err := runGit(remote, "config", "receive.advertisePushOptions", "true"); err != nil {
		t.Fatalf("enable push options on remote: %v", err)
	}
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	if _, err := runGit(repo.Dir, "push", remote, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("seed trunk: %v", err)
	}
	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add feature")

	// Worktree config (what workspace attach writes) vs a shadowed local
	// value someone set with plain `git config`.
	for _, args := range [][]string{
		{"config", "extensions.worktreeConfig", "true"},
		{"config", "--worktree", "runko.workspace", "real-ws"},
		{"config", "--local", "runko.workspace", "fake-ws"},
	} {
		if _, err := runGit(repo.Dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	var warnings strings.Builder
	oldWarn := warnWriter
	warnWriter = &warnings
	defer func() { warnWriter = oldWarn }()

	// The push itself fails only if the remote refuses push options; the
	// warning must fire either way before the transport runs.
	_, pushErr := PushChange(repo.Dir, remote, "main")
	if pushErr != nil {
		t.Fatalf("PushChange: %v", pushErr)
	}
	if !strings.Contains(warnings.String(), `"fake-ws"`) || !strings.Contains(warnings.String(), `"real-ws"`) {
		t.Fatalf("expected a warning naming both values, got %q", warnings.String())
	}
	if !strings.Contains(warnings.String(), "worktree value wins") {
		t.Fatalf("warning should say which value wins: %q", warnings.String())
	}
}

// TestCreateChangeRefusesSparseSkippedFiles: `git add -A` in a sparse-cone
// worktree skips out-of-cone paths with only an advisory warning, so the
// commit silently omitted them - easy way to lose work (2026-07-08 dogfood
// review). change create must refuse instead.
func TestCreateChangeRefusesSparseSkippedFiles(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("proj/keep.txt", "in cone\n")
	repo.WriteFile("elsewhere/other.txt", "outside cone\n")
	repo.Commit("initial")
	if _, err := runGit(repo.Dir, "sparse-checkout", "set", "--cone", "proj"); err != nil {
		t.Fatalf("sparse-checkout set: %v", err)
	}

	repo.WriteFile("proj/keep.txt", "edited in cone\n")
	repo.WriteFile("orphan-dir/README.md", "outside the cone\n")

	_, err := CreateChange(repo.Dir, "edit both sides", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "outside_sparse_cone" {
		t.Fatalf("want outside_sparse_cone, got %v", err)
	}
	if !strings.Contains(ce.Message, "orphan-dir/README.md") {
		t.Fatalf("error must name the skipped file, got %q", ce.Message)
	}
}

// TestCreateChangeRefusesBuildArtifact (FIX #4): a stray `go build` output
// binary at the repo root - executable + binary content - must be refused by
// name, not swept into the whole-tree commit; --allow-large is the escape.
func TestCreateChangeRefusesBuildArtifact(t *testing.T) {
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("proj/keep.go", "package proj\n")
	repo.Commit("initial")

	// The artifact: executable bit + binary content (a NUL byte), repo root.
	if err := os.WriteFile(filepath.Join(repo.Dir, "runko"), []byte("\x7fELF\x00\x00binary"), 0o755); err != nil {
		t.Fatalf("write artifact: %v", err)
	}
	repo.WriteFile("proj/keep.go", "package proj // real edit\n")

	_, err := CreateChange(repo.Dir, "add a feature", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "suspect_artifact" {
		t.Fatalf("want suspect_artifact, got %v", err)
	}
	if !strings.Contains(ce.Message, "runko") || !strings.Contains(ce.Message, "executable binary") {
		t.Fatalf("error must name the artifact and why, got %q", ce.Message)
	}

	// --allow-large lets an intentional addition through.
	if _, err := CreateChange(repo.Dir, "add a feature", true); err != nil {
		t.Fatalf("allowLarge should permit the commit: %v", err)
	}
}

// TestAmendChangeFoldsWorkKeepsChangeID (FIX #6): amend folds the working
// tree into HEAD's existing Change, preserving its Change-Id, and works with
// NO configured git author (the identity fallback raw `git commit --amend`
// lacked).
func TestAmendChangeFoldsWorkKeepsChangeID(t *testing.T) {
	repo := gitfixture.New(t) // no configureIdentity: exercise the no-git-author path
	repo.WriteFile("proj/a.go", "package proj\n")
	repo.Commit("seed")
	seed := mustGit(t, repo.Dir, "rev-parse", "HEAD")

	repo.WriteFile("proj/a.go", "package proj // feature edit\n")
	id, err := CreateChange(repo.Dir, "feature", false)
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}

	repo.WriteFile("proj/b.go", "package proj // folded in\n")
	got, err := AmendChange(repo.Dir, "", false)
	if err != nil {
		t.Fatalf("AmendChange: %v", err)
	}
	if got != id {
		t.Fatalf("amend must keep the Change-Id: %s -> %s", id, got)
	}
	if n := mustGit(t, repo.Dir, "rev-list", "--count", seed+"..HEAD"); n != "1" {
		t.Fatalf("want exactly 1 commit above seed after amend (folded, not stacked), got %s", n)
	}
	if out := mustGit(t, repo.Dir, "show", "--stat", "HEAD"); !strings.Contains(out, "b.go") {
		t.Fatalf("amended commit missing the folded file:\n%s", out)
	}
	if msg := mustGit(t, repo.Dir, "log", "-1", "--format=%B"); !strings.Contains(msg, id) {
		t.Fatalf("Change-Id trailer not preserved:\n%s", msg)
	}
}

// TestTransportRejectionMapsProxyFailure (FIX #2): the opaque Cloudflare
// chunked-pack failure becomes a structured error carrying the postBuffer
// remedy, while a daemon POLICY rejection and an auth failure pass through.
func TestTransportRejectionMapsProxyFailure(t *testing.T) {
	proxy := errors.New("push to refs/for/main: git push: exit status 1: error: RPC failed; HTTP 400 curl 22 The requested URL returned error: 400\nsend-pack: unexpected disconnect while reading sideband packet\nfatal: the remote end hung up unexpectedly")
	ce := transportRejection(proxy)
	if ce == nil || ce.Code != "push_transport_failed" {
		t.Fatalf("want push_transport_failed, got %v", ce)
	}
	if !strings.Contains(ce.Suggestion, "postBuffer") {
		t.Fatalf("suggestion must carry the postBuffer remedy: %q", ce.Suggestion)
	}

	policy := errors.New("git push: exit status 1: remote: change I123 is outside this workspace's affinity {cli}\nremote: error: hook declined to update refs/for/main")
	if got := transportRejection(policy); got != nil {
		t.Fatalf("a daemon policy rejection must pass through, not map to transport: %+v", got)
	}
	if got := transportRejection(errors.New("fatal: Authentication failed for 'https://...': HTTP 401")); got != nil {
		t.Fatalf("an auth failure must not get the postBuffer remedy: %+v", got)
	}
}

// installRejectHook installs a pre-receive hook on a bare remote that
// prints msg to stderr (one line per \n) and exits 1 - the house pattern
// for simulating a daemon policy refusal without runkod.
func installRejectHook(t *testing.T, bare, msg string) {
	t.Helper()
	hook := filepath.Join(bare, "hooks", "pre-receive")
	// Git prefixes each hook stderr line with "remote: "; do not bake that
	// prefix into the script (matches how a shell hook would print).
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, line := range strings.Split(strings.TrimRight(msg, "\n"), "\n") {
		b.WriteString("echo '")
		b.WriteString(strings.ReplaceAll(line, "'", `'"'"'`))
		b.WriteString("' >&2\n")
	}
	b.WriteString("exit 1\n")
	if err := os.WriteFile(hook, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}
}

// TestPushChangeClosedWorkspaceTeachesCarryOver: a closed-workspace
// refusal from the remote gains a client recovery block (fresh workspace +
// format-patch carry + re-push) exactly once. An auto-snapshot that hits
// the same refusal first must not double the block.
func TestPushChangeClosedWorkspaceTeachesCarryOver(t *testing.T) {
	remote := newBareRemote(t)
	if _, err := runGit(remote, "config", "receive.advertisePushOptions", "true"); err != nil {
		t.Fatalf("enable push options: %v", err)
	}

	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	repo.Run("remote add origin " + remote)
	if _, err := runGit(repo.Dir, "push", "origin", "main"); err != nil {
		t.Fatalf("seed remote main: %v", err)
	}

	// Refuse every subsequent push with the funnel's closed-workspace text.
	installRejectHook(t, remote, ""+
		`workspace "change-verb-ux" is closed - its task concluded, and one workspace carries one task`+"\n"+
		`  -> start the new task in a fresh workspace: runko workspace create --name <new> --project <p> ... --by <you>`)

	// Bind a workspace so auto-snapshot runs and also hits the closed refusal.
	if _, err := runGit(repo.Dir, "config", "runko.workspace", "change-verb-ux"); err != nil {
		t.Fatalf("bind workspace: %v", err)
	}
	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("stacked child still local\n\nChange-Id: " + fakeChangeID("closed-workspace-stacked"))

	var warnBuf strings.Builder
	prev := warnWriter
	warnWriter = &warnBuf
	defer func() { warnWriter = prev }()

	_, err := pushChange(repo.Dir, "origin", "main", false, true)
	if err == nil {
		t.Fatal("expected closed-workspace push to fail")
	}
	combined := warnBuf.String() + "\n" + err.Error()
	if !strings.Contains(err.Error(), `workspace "change-verb-ux" is closed`) {
		t.Fatalf("push error must keep the remote refusal text:\n%s", err)
	}
	// Recovery appears once across the auto-snapshot warning + push error.
	if n := strings.Count(combined, "git format-patch --stdout"); n != 1 {
		t.Fatalf("want format-patch recovery exactly once, got %d in:\n%s", n, combined)
	}
	if !strings.Contains(err.Error(), `git format-patch --stdout origin/main..HEAD | git -C "$(runko workspace path <new-task>)" am -3`) {
		t.Fatalf("recovery must teach format-patch carry via workspace path:\n%s", err)
	}
	if !strings.Contains(err.Error(), "runko workspace create --name <new-task>") {
		t.Fatalf("recovery must name workspace create:\n%s", err)
	}
	if !strings.Contains(err.Error(), "runko change push -w <new-task>") {
		t.Fatalf("recovery must name push from the new workspace:\n%s", err)
	}
	if strings.Contains(warnBuf.String(), "git format-patch --stdout") {
		t.Fatalf("auto-snapshot warning must not carry the recovery block:\n%s", warnBuf.String())
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "workspace_closed" {
		t.Fatalf("want workspace_closed clierr, got %v", err)
	}
}

// TestPushChangeUnrelatedPolicyRefusalHasNoCarryOver: a generic policy
// rejection must not grow the closed-workspace carry-over recovery.
func TestPushChangeUnrelatedPolicyRefusalHasNoCarryOver(t *testing.T) {
	remote := newBareRemote(t)

	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	repo.Run("remote add origin " + remote)
	if _, err := runGit(repo.Dir, "push", "origin", "main"); err != nil {
		t.Fatalf("seed remote main: %v", err)
	}
	installRejectHook(t, remote, ""+
		`change I123 is outside this workspace's affinity {cli}`+"\n"+
		`error: hook declined to update refs/for/main`)

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("feature\n\nChange-Id: " + fakeChangeID("unrelated-policy-refusal"))

	_, err := pushChange(repo.Dir, "origin", "main", false, false)
	if err == nil {
		t.Fatal("expected policy refusal")
	}
	msg := err.Error()
	if strings.Contains(msg, "git format-patch") || strings.Contains(msg, "workspace_closed") ||
		strings.Contains(msg, "carry commits") || strings.Contains(msg, "<new-task>") {
		t.Fatalf("unrelated policy refusal must not get closed-workspace recovery:\n%s", msg)
	}
	if !strings.Contains(msg, "outside this workspace's affinity") {
		t.Fatalf("must keep the remote policy text:\n%s", msg)
	}
}

// TestAnnotateClosedWorkspacePushUnit pins the matcher and the no-op
// path without a full git push.
func TestAnnotateClosedWorkspacePushUnit(t *testing.T) {
	closed := errors.New(`git push: exit status 1: remote: workspace "done-task" is closed - its task concluded`)
	got := annotateClosedWorkspacePush(closed, "origin", "trunk")
	var ce *clierr.Error
	if !errors.As(got, &ce) || ce.Code != "workspace_closed" {
		t.Fatalf("want workspace_closed, got %v", got)
	}
	if !strings.Contains(got.Error(), "origin/trunk..HEAD") {
		t.Fatalf("recovery must use the push's trunk name: %s", got)
	}
	// Second annotate is a no-op (single recovery block).
	again := annotateClosedWorkspacePush(got, "origin", "trunk")
	if strings.Count(again.Error(), "git format-patch --stdout") != 1 {
		t.Fatalf("annotate must be idempotent:\n%s", again)
	}

	other := errors.New(`git push: exit status 1: remote: Direct pushes to "main" are disabled - trunk is closed to direct push`)
	if got := annotateClosedWorkspacePush(other, "origin", "main"); got != other {
		t.Fatalf("trunk-closed phrasing must not match: %v", got)
	}
	affinity := errors.New(`remote: change I123 is outside this workspace's affinity {cli}`)
	if got := annotateClosedWorkspacePush(affinity, "origin", "main"); got != affinity {
		t.Fatalf("affinity refusal must not match: %v", got)
	}
}
