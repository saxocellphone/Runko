// runko workspace: CitC-class workspaces as upstream Git, configured
// (docs/design.md §12.3 Phase A, §28.3 stage 12b). One blobless clone pays
// the object store once; each workspace is a git worktree with its own
// sparse cone, base revision, snapshot ref, and registry row - switching
// workstreams is `cd`, not a stash-and-branch dance.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
)

// WorkspaceInfo mirrors runkod's workspace API response (the Workspace
// registry row plus checkout-building fields), decoded by the Go structs'
// own exported names per docs/cli-contract.md's convention.
type WorkspaceInfo struct {
	ID              string
	Owner           string
	BaseRevision    string
	ProjectAffinity []string
	WriteAllowlist  []string
	SnapshotRef     string
	Status          string
	SparsePatterns  []string
	RepoPath        string
	TrunkRef        string
	Branches        []string
}

// snapshotSubjectPrefix marks a commit as a workspace snapshot - snapshots
// amend by default (§12.2), so `workspace snapshot` amends when the branch
// tip already carries this marker and commits fresh otherwise.
const snapshotSubjectPrefix = "runko workspace snapshot"

// apiJSON does one authed JSON round-trip against runkod, decoding 4xx
// bodies into structured clierr.Errors like land.go/approve.go do.
func apiJSON(ctx context.Context, client *http.Client, method, urlStr, token string, body interface{}, out interface{}) error {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", authHeaderValue(token))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("contact %s: %w", urlStr, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return &clierr.Error{Code: "not_found", Message: fmt.Sprintf("%s: not found", urlStr)}
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		var ce clierr.Error
		if err := json.NewDecoder(resp.Body).Decode(&ce); err != nil || ce.Code == "" {
			return fmt.Errorf("%s returned %d", urlStr, resp.StatusCode)
		}
		return &ce
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s returned %d", urlStr, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// composeRemoteURL builds the smart-HTTP remote from the runkod base URL +
// the repo mount path the workspace API reports. CREDENTIAL-NEUTRAL by
// §12.7: the URL never carries userinfo - auth is injected per invocation
// (gitauth.go), so one shared store serves every principal on the machine
// without misattributing anyone's push.
func composeRemoteURL(runkodURL, repoPath string) (string, error) {
	u, err := url.Parse(runkodURL)
	if err != nil {
		return "", fmt.Errorf("parse --runkod-url: %w", err)
	}
	u.User = nil
	u.Path = strings.TrimSuffix(u.Path, "/") + "/" + repoPath + "/"
	return u.String(), nil
}

// absWorkspacePaths pins dir and cloneDir to the CALLER's cwd before any
// git subprocess runs elsewhere: `git -C <cloneDir> worktree add <dir>`
// resolves a relative dir against cloneDir, so the default relative --dir
// silently landed the worktree INSIDE the shared clone (found live; every
// test passed absolute paths).
func absWorkspacePaths(cloneDir, dir string) (string, string, error) {
	absClone, err := filepath.Abs(cloneDir)
	if err != nil {
		return "", "", err
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	return absClone, absDir, nil
}

// ensureSharedClone makes sure cloneDir holds the one blobless clone every
// workspace worktree hangs off (§12.3: "one blobless clone, N git
// worktrees"). --no-checkout: the shared clone itself never materializes a
// working tree; only worktrees do, each under its own sparse cone.
// authEnv carries the CALLING verb's credential for the initial clone
// (gitauth.go) - the one network op that runs before any config exists.
func ensureSharedClone(cloneDir, remoteURL string, authEnv []string) error {
	if _, err := os.Stat(filepath.Join(cloneDir, ".git")); err == nil {
		// Retrofits for clones that predate a config decision: the verb
		// nudge (2026-07-14) and §12.7's credential-neutral remote - a
		// pre-§12.7 store still carries its creator's token in the origin
		// URL, misattributing every other principal's push; strip it and
		// let the helper answer instead.
		if err := neutralizeStoreRemote(cloneDir); err != nil {
			return err
		}
		return installWorkspaceHooks(cloneDir)
	}
	if _, err := runGitEnv(".", authEnv, "clone", "--filter=blob:none", "--no-checkout", remoteURL, cloneDir); err != nil {
		return fmt.Errorf("blobless clone: %w", err)
	}
	// Per-worktree sparse cones and per-worktree runko.* config both need
	// worktree-scoped configuration.
	if _, err := runGit(cloneDir, "config", "extensions.worktreeConfig", "true"); err != nil {
		return err
	}
	// The store is credential-neutral (§12.7): raw git in any worktree -
	// including the blobless clone's lazy blob fetches - asks this helper,
	// which resolves the INVOKING principal's stored login.
	if _, err := runGit(cloneDir, "config", "credential.helper", credentialHelperSpec()); err != nil {
		return err
	}
	// On a public_read org (§15.2), a credential-less read gets an
	// anonymous 200 - git never sees the 401 that would make it ask the
	// credential helper, and the anonymous advertisement HIDES
	// refs/workspaces, which is exactly what this clone must fetch.
	// proactiveAuth (git >= 2.46) asks the helper up front; older
	// gits ignore the unknown key and keep today's behavior (fine on
	// private orgs, where the challenge fires).
	if _, err := runGit(cloneDir, "config", "http.proactiveAuth", "basic"); err != nil {
		return err
	}
	return installWorkspaceHooks(cloneDir)
}

// neutralizeStoreRemote strips userinfo from a pre-§12.7 store's origin
// URL and stamps the credential helper in its place. Idempotent; a store
// that is already neutral is untouched.
func neutralizeStoreRemote(cloneDir string) error {
	remote, err := runGit(cloneDir, "config", "remote.origin.url")
	if err != nil {
		return nil // no origin (hand-built store): nothing to neutralize
	}
	u, err := url.Parse(remote)
	if err != nil || u.User == nil {
		return nil
	}
	u.User = nil
	if _, err := runGit(cloneDir, "config", "remote.origin.url", u.String()); err != nil {
		return err
	}
	if _, err := runGit(cloneDir, "config", "credential.helper", credentialHelperSpec()); err != nil {
		return err
	}
	return nil
}

// installWorkspaceHooks stamps the shared clone's hooks dir (worktrees
// inherit it) with the advisory pre-commit verb nudge, so a raw
// `git commit` in ANY workspace worktree answers with the native verbs
// (§6.9 UX one moment earlier; the materialized environment should teach
// the verbs, not just AGENTS.md). Advisory only - a foreign pre-commit
// hook is silently left in place, and the nudge never blocks a commit.
// The Change-Id commit-msg hook rides along (§6.10): raw git commits in a
// materialized worktree should be pushable without the separate
// `runko doctor --install-hook` step onboarding used to require - runko's
// own verbs stamp Change-Ids regardless, so this only serves the raw-git
// loop. Same politeness: an existing commit-msg hook (Gerrit's own mints
// Change-Ids too) is never overwritten.
func installWorkspaceHooks(cloneDir string) error {
	if _, err := InstallVerbNudgeHook(cloneDir); err != nil {
		return err
	}
	if _, err := InstallChangeIDHookIfAbsent(cloneDir); err != nil {
		return err
	}
	return nil
}

// materializeWorktree adds a worktree for the workspace at dir, checked out
// at startPoint under the workspace's sparse cone, and stamps the worktree
// config so snapshot/update-base know which workspace they're in.
// authEnv rides every subprocess: in a blobless clone, sparse-checkout and
// checkout LAZILY FETCH blobs from the promisor remote - any of them is a
// network call needing the calling verb's credential (found live: the
// daemon e2e's checkout 401ed with only the obvious fetch/push covered).
func materializeWorktree(cloneDir, dir string, info WorkspaceInfo, startPoint, wsBranch string, authEnv []string) error {
	// A previously-deleted workspace directory leaves stale worktree
	// metadata behind; prune first so attach-after-laptop-loss just works.
	if _, err := runGit(cloneDir, "worktree", "prune"); err != nil {
		return err
	}
	// Per-branch local branch name: git refuses one branch checked out in
	// two worktrees, and parallel work IS two worktrees on two branches of
	// the same workspace (§12.2 workspace branches).
	branch := "ws/" + info.ID + "/" + wsBranch
	// -B: (re)set the branch to startPoint - on attach, that's the snapshot
	// tip, which IS the restore semantic (§12.2's cloud-primary identity).
	if _, err := runGit(cloneDir, "worktree", "add", "--no-checkout", "-B", branch, dir, startPoint); err != nil {
		if strings.Contains(err.Error(), "already used by worktree") {
			// Single-writer per branch (§12.2): the branch is materialized
			// in another worktree on this machine. Concurrent same-branch
			// editing stays explicit (--shared, future), never accidental.
			return &clierr.Error{
				Code: "branch_in_use", Field: "branch",
				Message:    fmt.Sprintf("workspace branch %q is already attached in another worktree on this machine", wsBranch),
				Suggestion: "work there, or fork a parallel line with `runko workspace branch <name>`",
			}
		}
		return fmt.Errorf("worktree add: %w", err)
	}
	if len(info.SparsePatterns) > 0 {
		args := append([]string{"sparse-checkout", "set", "--cone"}, info.SparsePatterns...)
		if _, err := runGitEnv(dir, authEnv, args...); err != nil {
			return fmt.Errorf("sparse-checkout set: %w", err)
		}
	}
	if _, err := runGitEnv(dir, authEnv, "checkout"); err != nil {
		return fmt.Errorf("checkout: %w", err)
	}
	for k, v := range map[string]string{
		"runko.workspace": info.ID,
		"runko.trunk":     info.TrunkRef,
		"runko.branch":    wsBranch,
	} {
		if _, err := runGit(dir, "config", "--worktree", k, v); err != nil {
			return err
		}
	}
	return nil
}

// cloneJJCheckout lays down the standalone FULL clone a --jj workspace
// lives in. jj refuses to colocate inside a git worktree, so unlike the
// default mode there is no shared store - each --jj workspace is its own
// clone, with the same store contract as ensureSharedClone: credential-
// neutral remote answered by the helper, proactive auth for public_read
// orgs (§15.2), and both identity hooks for the raw-git loop (§6.9, §6.10).
// Full, not blobless: jj's backend reads the object store directly and
// cannot lazy-fetch promisor objects, so a partial clone breaks the moment
// jj materializes a commit whose blobs git never checked out (found by
// TestWorkspaceAttachJJColocated). The jj binary is checked FIRST: failing
// before the network round-trips beats failing after them.
func cloneJJCheckout(remoteURL, dir string, authEnv []string) error {
	if _, err := exec.LookPath("jj"); err != nil {
		return &clierr.Error{
			Code: "jj_not_found", Field: "jj",
			Message:    "--jj needs the jj binary on PATH to set up a colocated checkout",
			Suggestion: "install jj (https://jj-vcs.github.io), or drop --jj for a plain-git worktree",
		}
	}
	if _, err := runGitEnv(".", authEnv, "clone", remoteURL, dir); err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	for _, kv := range [][2]string{
		{"credential.helper", credentialHelperSpec()},
		{"http.proactiveAuth", "basic"},
	} {
		if _, err := runGit(dir, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return installWorkspaceHooks(dir)
}

// finishJJCheckout colocates jj over the clone and parks it on startPoint
// (§21: runko sets the surgical client up rather than leaving it
// bring-your-own): the trailer template so Change-Ids derive from jj change
// ids (§7.4), the cone mirrored via `jj sparse` (jj owns working-copy
// materialization - git's sparse-checkout machinery doesn't apply), a fresh
// empty @ on startPoint, and the runko.* binding in PLAIN config scope - a
// standalone clone has no worktree config, and every reader (push
// provenance, watch, sync) does plain lookups.
func finishJJCheckout(dir string, info WorkspaceInfo, startPoint, wsBranch string) error {
	// The snapshot line gets a real local branch (the worktree-mode naming):
	// jj imports branches as bookmarks, and that import is what makes a
	// non-branch commit - an attach's snapshot tip, fetched from
	// refs/workspaces/* - visible to jj revsets at all.
	if _, err := runGit(dir, "branch", "-f", "ws/"+info.ID+"/"+wsBranch, startPoint); err != nil {
		return fmt.Errorf("anchor %s: %w", short(startPoint), err)
	}
	if err := jjGitInitColocate(dir); err != nil {
		return err
	}
	if err := SetupJJChangeIDs(dir); err != nil {
		return err
	}
	// Same no-identity fallback as the git verbs (§7.5): jj refuses to push
	// empty-identity commits, and materializing must work on a fresh VM. A
	// configured identity always wins - the repo scope is only written when
	// jj resolves none at all.
	if email, _ := runJJ(dir, "config", "get", "user.email"); strings.TrimSpace(email) == "" {
		for _, kv := range [][2]string{{"user.name", "Runko"}, {"user.email", "runko@localhost"}} {
			if _, err := runJJ(dir, "config", "set", "--repo", kv[0], kv[1]); err != nil {
				return err
			}
		}
	}
	if patterns := jjSparsePatterns(dir, info.SparsePatterns, startPoint); len(patterns) > 0 {
		args := []string{"sparse", "set", "--clear"}
		for _, p := range patterns {
			args = append(args, "--add", p)
		}
		if _, err := runJJ(dir, args...); err != nil {
			return fmt.Errorf("mirror the sparse cone into jj: %w", err)
		}
	}
	if _, err := runJJ(dir, "new", startPoint); err != nil {
		return fmt.Errorf("start the working copy at %s: %w", short(startPoint), err)
	}
	for k, v := range map[string]string{
		"runko.workspace": info.ID,
		"runko.trunk":     info.TrunkRef,
		"runko.branch":    wsBranch,
	} {
		if _, err := runGit(dir, "config", k, v); err != nil {
			return err
		}
	}
	return nil
}

// jjSparsePatterns translates the workspace cone into jj prefix patterns.
// Cone mode always materializes the repo root's FILES alongside the cone
// dirs; jj has no "root files only" pattern, so they're enumerated from
// startPoint's tree. A root-spanning pattern ("." - the whole tree) means
// there is nothing to narrow: return nil and keep jj's default
// (everything). The empty pattern is the root project's cone - root files
// only, which the enumeration already covers.
func jjSparsePatterns(dir string, cone []string, rev string) []string {
	if len(cone) == 0 {
		return nil
	}
	var patterns []string
	for _, p := range cone {
		if p == "." {
			return nil
		}
		if p == "" {
			continue
		}
		patterns = append(patterns, p)
	}
	out, err := runGit(dir, "ls-tree", rev)
	if err != nil {
		return patterns
	}
	for _, line := range strings.Split(out, "\n") {
		// <mode> SP blob SP <sha> TAB <name>
		tab := strings.IndexByte(line, '\t')
		if tab < 0 || !strings.Contains(line[:tab], " blob ") {
			continue
		}
		patterns = append(patterns, line[tab+1:])
	}
	return patterns
}

// MaterializeOptions is where a workspace lands on this machine. Empty
// CloneDir/Dir take the §12.7 managed home - the pre-§12.7 cwd-relative
// defaults ("mono", worktree named into the caller's cwd) are gone: run
// from the wrong directory they materialized workspaces into whatever
// checkout the caller stood in, which is exactly how the in-tree sprawl
// happened (migration finding #49).
type MaterializeOptions struct {
	CloneDir    string // shared blobless store; "" = <home>/<host>/<org>/<repo>/.store
	Dir         string // worktree; "" = <home>/<host>/<org>/<repo>/<workspace>[@<branch>]
	ForceNested bool   // materialize inside another git checkout anyway
	JJ          bool   // standalone jj colocated checkout instead of a worktree off the store
}

// preflight validates EXPLICIT paths before any server round-trip: a
// placement refusal after the registration POST would strand a name on
// the server. Managed defaults need the API response (repo mount path)
// and are guarded in resolve instead - a managed home nested inside a
// checkout is the pathological case, an explicit --dir into one is the
// common misuse.
func (o MaterializeOptions) preflight() error {
	if o.JJ && o.CloneDir != "" {
		return &clierr.Error{
			Code: "jj_no_shared_store", Field: "clone-dir",
			Message:    "--jj materializes a standalone colocated checkout; there is no shared store to point --clone-dir at",
			Suggestion: "drop --clone-dir (--dir still places the checkout)",
		}
	}
	for _, p := range []string{o.CloneDir, o.Dir} {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if err := refuseNestedCheckout(abs, o.ForceNested); err != nil {
			return err
		}
	}
	return nil
}

// resolve pins explicit paths to the caller's cwd (the worktree-inside-
// shared-clone lesson, absWorkspacePaths) or lays defaults out under the
// managed home, then applies the nested-checkout guard to both.
func (o MaterializeOptions) resolve(runkodURL, repoPath, wsName, branch string) (cloneDir, dir string, err error) {
	cloneDir, dir = o.CloneDir, o.Dir
	if cloneDir == "" || dir == "" {
		mStore, mDir, err := managedPaths(runkodURL, repoPath, wsName, branch)
		if err != nil {
			return "", "", err
		}
		if cloneDir == "" {
			cloneDir = mStore
		}
		if dir == "" {
			dir = mDir
		}
	}
	if cloneDir, dir, err = absWorkspacePaths(cloneDir, dir); err != nil {
		return "", "", err
	}
	if err := refuseNestedCheckout(cloneDir, o.ForceNested); err != nil {
		return "", "", err
	}
	if err := refuseNestedCheckout(dir, o.ForceNested); err != nil {
		return "", "", err
	}
	return cloneDir, dir, nil
}

// WorkspaceCreate registers the workspace and materializes its worktree.
// The returned dir is where it landed (callers may have passed none).
func WorkspaceCreate(ctx context.Context, client *http.Client, runkodURL, token, name, owner string, projects []string, opts MaterializeOptions) (WorkspaceInfo, string, error) {
	if err := opts.preflight(); err != nil {
		return WorkspaceInfo{}, "", err
	}
	var info WorkspaceInfo
	err := apiJSON(ctx, client, http.MethodPost, strings.TrimSuffix(runkodURL, "/")+"/api/workspaces", token,
		map[string]interface{}{"name": name, "owner": owner, "projects": projects}, &info)
	if err != nil {
		return WorkspaceInfo{}, "", err
	}
	cloneDir, dir, err := opts.resolve(runkodURL, info.RepoPath, info.ID, "head")
	if err != nil {
		return WorkspaceInfo{}, "", err
	}
	remoteURL, err := composeRemoteURL(runkodURL, info.RepoPath)
	if err != nil {
		return WorkspaceInfo{}, "", err
	}
	authEnv := gitAuthConfigEnv(runkodURL, token)
	if opts.JJ {
		if err := cloneJJCheckout(remoteURL, dir, authEnv); err != nil {
			return WorkspaceInfo{}, "", err
		}
		if err := finishJJCheckout(dir, info, info.BaseRevision, "head"); err != nil {
			return WorkspaceInfo{}, "", err
		}
		_ = recordMaterialization(Materialization{
			Workspace: info.ID, Branch: "head", Path: dir, RunkodURL: runkodURL,
		})
		return info, dir, nil
	}
	if err := ensureSharedClone(cloneDir, remoteURL, authEnv); err != nil {
		return WorkspaceInfo{}, "", err
	}
	// §12.7 auto-gc + recycle: sweep unambiguously reclaimable
	// materializations on this store (bounded), preferring to REBIND one
	// over deleting it - its ignored caches (node_modules, build output)
	// are the expensive bytes a fresh worktree would pay to rebuild.
	materialized := false
	if cand := autoGCAndRecycle(ctx, client, runkodURL, token, cloneDir, true); cand != nil {
		switch err := rebindWorktree(cloneDir, *cand, dir, info, info.BaseRevision, "head", authEnv); {
		case err == nil:
			fmt.Fprintf(os.Stderr, "recycled %s (ignored caches preserved)\n", cand.Path)
			materialized = true
		case errors.Is(err, errRecycleUnavailable):
			// Fall through to a fresh worktree; the candidate stays put.
		default:
			return WorkspaceInfo{}, "", err
		}
	}
	if !materialized {
		if err := materializeWorktree(cloneDir, dir, info, info.BaseRevision, "head", authEnv); err != nil {
			return WorkspaceInfo{}, "", err
		}
	}
	// Cache, never truth (§12.7): a registry write failure must not fail
	// the create that already did the real work.
	_ = recordMaterialization(Materialization{
		Workspace: info.ID, Branch: "head", Path: dir, Store: cloneDir, RunkodURL: runkodURL,
	})
	return info, dir, nil
}

// WorkspaceAttach restores a workspace on this machine from its registry
// row and snapshot ref - the §12.2 "attach from anywhere" contract: delete
// the directory (or lose the laptop) and nothing durable is lost.
func WorkspaceAttach(ctx context.Context, client *http.Client, runkodURL, token, id, branch string, opts MaterializeOptions) (WorkspaceInfo, string, error) {
	var info WorkspaceInfo
	err := apiJSON(ctx, client, http.MethodGet, strings.TrimSuffix(runkodURL, "/")+"/api/workspaces/"+id, token, nil, &info)
	if err != nil {
		if ce := (*clierr.Error)(nil); asClierr(err, &ce) && ce.Code == "not_found" {
			return WorkspaceInfo{}, "", &clierr.Error{
				Code: "not_found", Field: "workspace",
				Message:    fmt.Sprintf("no workspace %q is registered", id),
				Suggestion: "list yours with `runko workspace list`, or create it with `runko workspace create`",
			}
		}
		return WorkspaceInfo{}, "", err
	}
	if branch == "" {
		branch = "head"
	}
	cloneDir, dir, err := opts.resolve(runkodURL, info.RepoPath, info.ID, branch)
	if err != nil {
		return WorkspaceInfo{}, "", err
	}
	remoteURL, err := composeRemoteURL(runkodURL, info.RepoPath)
	if err != nil {
		return WorkspaceInfo{}, "", err
	}
	authEnv := gitAuthConfigEnv(runkodURL, token)
	if opts.JJ {
		if err := cloneJJCheckout(remoteURL, dir, authEnv); err != nil {
			return WorkspaceInfo{}, "", err
		}
		// Prefer the branch's snapshot tip; a branch that never snapshotted
		// restores at the workspace's base revision.
		snapshotRef := "refs/workspaces/" + id + "/" + branch
		startPoint := info.BaseRevision
		if _, err := runGitEnv(dir, authEnv, "fetch", "origin", "+"+snapshotRef+":"+snapshotRef); err == nil {
			if sha, err := runGit(dir, "rev-parse", snapshotRef); err == nil {
				startPoint = sha
			}
		}
		if err := finishJJCheckout(dir, info, startPoint, branch); err != nil {
			return WorkspaceInfo{}, "", err
		}
		_ = recordMaterialization(Materialization{
			Workspace: info.ID, Branch: branch, Path: dir, RunkodURL: runkodURL,
		})
		return info, dir, nil
	}
	if err := ensureSharedClone(cloneDir, remoteURL, authEnv); err != nil {
		return WorkspaceInfo{}, "", err
	}
	// Attach sweeps too (§12.7 auto-gc), but always materializes fresh:
	// restore-from-snapshot is already the cheap path.
	autoGCAndRecycle(ctx, client, runkodURL, token, cloneDir, false)

	// Prefer the branch's snapshot tip; a branch that never snapshotted
	// restores at the workspace's base revision.
	snapshotRef := "refs/workspaces/" + id + "/" + branch
	startPoint := info.BaseRevision
	if _, err := runGitEnv(cloneDir, authEnv, "fetch", "origin", "+"+snapshotRef+":"+snapshotRef); err == nil {
		if sha, err := runGit(cloneDir, "rev-parse", snapshotRef); err == nil {
			startPoint = sha
		}
	}
	if err := materializeWorktree(cloneDir, dir, info, startPoint, branch, authEnv); err != nil {
		return WorkspaceInfo{}, "", err
	}
	_ = recordMaterialization(Materialization{
		Workspace: info.ID, Branch: branch, Path: dir, Store: cloneDir, RunkodURL: runkodURL,
	})
	return info, dir, nil
}

// WorkspaceSnapshot commits all WIP in dir (amend-by-default, §12.2) and
// force-pushes it to the workspace's snapshot ref - durability through the
// same receive funnel as Changes, so policy and secret scan run BEFORE the
// bytes become durable.
func WorkspaceSnapshot(dir, message string) (ref string, err error) {
	// A bound jj colocated checkout (--jj) snapshots OUT-OF-BAND (watch.go's
	// mechanics: throwaway index, commit-tree on HEAD, push the sha) -
	// committing on the checked-out line would rewrite history behind jj's
	// back. Same ref, same amend-at-the-tip semantics. Checked BEFORE the
	// worktree lookup: without extensions.worktreeConfig, `git config
	// --worktree` silently degrades to --local and would read the jj
	// checkout's plain-scope binding as if it were a worktree.
	if isJJWorkspace(dir) {
		if plain, _ := runGit(dir, "config", "runko.workspace"); plain != "" {
			if message == "" {
				message = time.Now().UTC().Format(time.RFC3339)
			}
			ref, _, _, err := WorkspaceWatchSnapshot(dir, message, "")
			return ref, err
		}
	}
	id, err := runGit(dir, "config", "--worktree", "runko.workspace")
	if err != nil || id == "" {
		return "", &clierr.Error{
			Code: "not_a_workspace", Field: "dir",
			Message:    fmt.Sprintf("%s is not a runko workspace worktree", dir),
			Suggestion: "run inside a directory created by `runko workspace create` or `attach`",
		}
	}
	if _, err := runGit(dir, "add", "-A"); err != nil {
		return "", err
	}
	if message == "" {
		message = fmt.Sprintf("%s: %s", snapshotSubjectPrefix, time.Now().UTC().Format(time.RFC3339))
	} else {
		message = snapshotSubjectPrefix + ": " + message
	}
	// A snapshot commit must never fail on a machine with no git identity
	// configured (fresh VM, agent container - §12.5's "the glue CLI exists
	// to paper exactly these" edges). Fall back to a runko identity ONLY
	// when none is configured; a real user.name/user.email always wins.
	// One placeholder shared with `change create` (no more "Runko
	// Workspace"); it never reaches the mirror - the daemon re-stamps the
	// canonical landing identity at land time (§7.5).
	var identity []string
	if email, _ := runGit(dir, "config", "user.email"); email == "" {
		identity = []string{"-c", "user.name=Runko", "-c", "user.email=runko@localhost"}
	}
	commit := func(args ...string) error {
		_, err := runGit(dir, append(append(append([]string{}, identity...), "commit"), args...)...)
		return err
	}

	subject, _ := runGit(dir, "log", "-1", "--format=%s")
	staged, _ := runGit(dir, "status", "--porcelain")
	switch {
	case staged == "" && strings.HasPrefix(subject, snapshotSubjectPrefix):
		// Nothing new since the last snapshot commit - push as-is.
	case strings.HasPrefix(subject, snapshotSubjectPrefix):
		if err := commit("--amend", "-m", message); err != nil {
			return "", err
		}
	case staged == "":
		// Clean tree, no snapshot commit yet: the base itself is the snapshot.
	default:
		if err := commit("-m", message); err != nil {
			return "", err
		}
	}

	branch, _ := runGit(dir, "config", "--worktree", "runko.branch")
	if branch == "" {
		branch = "head" // worktrees from before workspace branches existed
	}
	ref = "refs/workspaces/" + id + "/" + branch
	// Force: amends rewrite the tip, and the snapshot ref's history is
	// exactly "latest durable WIP" (§12.2), not an append-only log.
	if _, err := runGitNet(dir, "push", "origin", "+HEAD:"+ref); err != nil {
		return "", fmt.Errorf("push snapshot: %w", err)
	}
	touchMaterialization(dir) // gc's --idle signal (§12.7)
	return ref, nil
}

// WorkspaceBranch starts a parallel line of work in this worktree
// (§12.2 workspace branches): switch to a fresh local branch (WIP rides
// along - git carries uncommitted changes across checkout -b), point the
// worktree's snapshot target at refs/workspaces/<id>/<name>, and snapshot
// immediately so the fork point is durable from second zero. Parallel in
// the full sense comes from attaching the other branch into a second
// worktree: `runko workspace attach <id> --branch head --dir <elsewhere>`.
func WorkspaceBranch(dir, name string) (ref string, err error) {
	// Workspace branches fork git worktrees off the shared store; a --jj
	// workspace is a standalone colocated checkout with neither. Its
	// parallel line is another standalone checkout of the same workspace -
	// and stacked (not parallel) work is jj's home turf. Checked before the
	// worktree lookup for the same --worktree-degrades-to-local reason as
	// WorkspaceSnapshot.
	if isJJWorkspace(dir) {
		if plain, _ := runGit(dir, "config", "runko.workspace"); plain != "" {
			return "", &clierr.Error{
				Code: "jj_checkout", Field: "dir",
				Message:    "workspace branches fork git worktrees; this is a standalone jj colocated checkout",
				Suggestion: "attach the parallel line as its own checkout: `runko workspace attach " + plain + " --jj --branch " + name + " --dir <elsewhere>` (stacked work needs no branch - jj new / runko change create)",
			}
		}
	}
	id, err := runGit(dir, "config", "--worktree", "runko.workspace")
	if err != nil || id == "" {
		return "", &clierr.Error{
			Code: "not_a_workspace", Field: "dir",
			Message:    fmt.Sprintf("%s is not a runko workspace worktree", dir),
			Suggestion: "run inside a directory created by `runko workspace create` or `attach`",
		}
	}
	if !workspaceBranchPattern.MatchString(name) {
		return "", &clierr.Error{
			Code: "invalid_branch_name", Field: "name",
			Message:    fmt.Sprintf("%q is not a valid workspace branch name", name),
			Suggestion: "one segment: letters, digits, dots, dashes, underscores; start with a letter or digit",
		}
	}
	if _, err := runGit(dir, "checkout", "-b", "ws/"+id+"/"+name); err != nil {
		return "", fmt.Errorf("switch to branch %s: %w", name, err)
	}
	if _, err := runGit(dir, "config", "--worktree", "runko.branch", name); err != nil {
		return "", err
	}
	return WorkspaceSnapshot(dir, "fork "+name)
}

// workspaceBranchPattern mirrors runkod's receive-side rule (workspace.go's
// workspaceIDPattern): the daemon would reject anything else anyway, this
// just names the problem before a network round-trip (§6.5).
var workspaceBranchPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// WorkspaceUpdateBase is §12.3's "sync base" row, surfaced as `runko
// workspace sync` (`update-base` stays as an alias): sync the workspace
// onto the trunk tip via SyncToTrunk (jj-aware; plain-git rebase with
// §6.6's abort-and-name-the-files conflict UX) and record the new base
// in the registry. Reads runko.workspace like `change push` does (plain
// config lookup, so worktree AND standalone jj-colocated bindings work).
func WorkspaceUpdateBase(ctx context.Context, client *http.Client, runkodURL, token, dir string) (newBase string, err error) {
	id, err := runGit(dir, "config", "runko.workspace")
	if err != nil || id == "" {
		return "", &clierr.Error{
			Code: "not_a_workspace", Field: "dir",
			Message:    fmt.Sprintf("%s is not bound to a runko workspace", dir),
			Suggestion: "runko workspace create/attach, or bind an existing checkout: git config runko.workspace <id>",
		}
	}
	trunk, err := runGit(dir, "config", "runko.trunk")
	if err != nil || trunk == "" {
		trunk = "main"
	}
	newBase, err = SyncToTrunk(dir, "origin", trunk)
	if err != nil {
		return "", err
	}
	err = apiJSON(ctx, client, http.MethodPost,
		strings.TrimSuffix(runkodURL, "/")+"/api/workspaces/"+id+"/base", token,
		map[string]string{"base_revision": newBase}, nil)
	if err != nil {
		return "", fmt.Errorf("record new base in registry: %w", err)
	}
	touchMaterialization(dir) // gc's --idle signal (§12.7)
	return newBase, nil
}

// WorkspaceDelete removes the workspace server-side: registry row + every
// refs/workspaces/<id>/* snapshot ref. The server refuses while the
// workspace has open changes (land or abandon first) and enforces
// owner-only for named principals. Local checkouts are the caller's to
// remove - the CLI never deletes directories it didn't create this run.
func WorkspaceDelete(ctx context.Context, client *http.Client, runkodURL, token, id string) error {
	return apiJSON(ctx, client, http.MethodDelete,
		strings.TrimSuffix(runkodURL, "/")+"/api/workspaces/"+id, token, nil, nil)
}

// WorkspaceList fetches the registry listing.
func WorkspaceList(ctx context.Context, client *http.Client, runkodURL, token string) ([]WorkspaceInfo, error) {
	var list []WorkspaceInfo
	err := apiJSON(ctx, client, http.MethodGet, strings.TrimSuffix(runkodURL, "/")+"/api/workspaces", token, nil, &list)
	return list, err
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// asClierr is errors.As sugar for the doubled-pointer dance.
func asClierr(err error, target **clierr.Error) bool {
	ce, ok := err.(*clierr.Error)
	if ok {
		*target = ce
	}
	return ok
}

// printWorkspaceStreamingGuidance is the §12.6 golden-path teach (decided
// 2026-07-14): the exact next commands that make this worktree's work
// visible live - §6.9's script pattern, printed at the moment the worktree
// is born. Human output only; --json callers never see it.
func printWorkspaceStreamingGuidance(w io.Writer, dir string) {
	fmt.Fprintln(w, "stream the work (§12.6) - run inside the worktree:")
	fmt.Fprintf(w, "  cd %s\n", dir)
	fmt.Fprintln(w, "  runko workspace watch &          # auto-snapshot loop: the workspace page follows WIP live")
	fmt.Fprintln(w, "  runko agent hooks --install      # agents: reads/edits/commands on the live activity feed (§12.6.1)")
}

// printWorkspaceLoop is §6.9's three commands, printed the moment a fresh
// checkout exists (§6.10) - the same script CONTRIBUTING.md and doctor's
// cheat-sheet teach, delivered when it is actually needed instead of
// waiting to be asked for.
func printWorkspaceLoop(w io.Writer) {
	fmt.Fprintln(w, "the loop:")
	fmt.Fprintln(w, "  runko change create -m \"<what and why>\"   # commit your work as one Change")
	fmt.Fprintln(w, "  runko change push                          # submit it (and its stack) for review")
	fmt.Fprintln(w, "  runko change requirements                  # owners + checks outstanding")
}
