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
	"fmt"
	"net/http"
	"net/url"
	"os"
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

// composeRemoteURL builds the authed smart-HTTP remote from the runkod base
// URL + deploy token + the repo mount path the workspace API reports -
// plain HTTP Basic, which every git client supports natively (§14.11).
func composeRemoteURL(runkodURL, token, repoPath string) (string, error) {
	u, err := url.Parse(runkodURL)
	if err != nil {
		return "", fmt.Errorf("parse --runkod-url: %w", err)
	}
	user, pass := gitUserPass(token)
	u.User = url.UserPassword(user, pass)
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
func ensureSharedClone(cloneDir, remoteURL string) error {
	if _, err := os.Stat(filepath.Join(cloneDir, ".git")); err == nil {
		return nil
	}
	if _, err := runGit(".", "clone", "--filter=blob:none", "--no-checkout", remoteURL, cloneDir); err != nil {
		return fmt.Errorf("blobless clone: %w", err)
	}
	// Per-worktree sparse cones and per-worktree runko.* config both need
	// worktree-scoped configuration.
	if _, err := runGit(cloneDir, "config", "extensions.worktreeConfig", "true"); err != nil {
		return err
	}
	// On a public_read org (§15.2), a credential-less read gets an
	// anonymous 200 - git never sees the 401 that would make it send the
	// URL-embedded credentials, and the anonymous advertisement HIDES
	// refs/workspaces, which is exactly what this clone must fetch.
	// proactiveAuth (git >= 2.46) sends the credentials up front; older
	// gits ignore the unknown key and keep today's behavior (fine on
	// private orgs, where the challenge fires).
	if _, err := runGit(cloneDir, "config", "http.proactiveAuth", "basic"); err != nil {
		return err
	}
	return nil
}

// materializeWorktree adds a worktree for the workspace at dir, checked out
// at startPoint under the workspace's sparse cone, and stamps the worktree
// config so snapshot/update-base know which workspace they're in.
func materializeWorktree(cloneDir, dir string, info WorkspaceInfo, startPoint, wsBranch string) error {
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
		if _, err := runGit(dir, args...); err != nil {
			return fmt.Errorf("sparse-checkout set: %w", err)
		}
	}
	if _, err := runGit(dir, "checkout"); err != nil {
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

// WorkspaceCreate registers the workspace and materializes its worktree.
func WorkspaceCreate(ctx context.Context, client *http.Client, runkodURL, token, name, owner string, projects []string, cloneDir, dir string) (WorkspaceInfo, error) {
	cloneDir, dir, err := absWorkspacePaths(cloneDir, dir)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	var info WorkspaceInfo
	err = apiJSON(ctx, client, http.MethodPost, strings.TrimSuffix(runkodURL, "/")+"/api/workspaces", token,
		map[string]interface{}{"name": name, "owner": owner, "projects": projects}, &info)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	remoteURL, err := composeRemoteURL(runkodURL, token, info.RepoPath)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if err := ensureSharedClone(cloneDir, remoteURL); err != nil {
		return WorkspaceInfo{}, err
	}
	if err := materializeWorktree(cloneDir, dir, info, info.BaseRevision, "head"); err != nil {
		return WorkspaceInfo{}, err
	}
	return info, nil
}

// WorkspaceAttach restores a workspace on this machine from its registry
// row and snapshot ref - the §12.2 "attach from anywhere" contract: delete
// the directory (or lose the laptop) and nothing durable is lost.
func WorkspaceAttach(ctx context.Context, client *http.Client, runkodURL, token, id, branch, cloneDir, dir string) (WorkspaceInfo, error) {
	cloneDir, dir, err := absWorkspacePaths(cloneDir, dir)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	var info WorkspaceInfo
	err = apiJSON(ctx, client, http.MethodGet, strings.TrimSuffix(runkodURL, "/")+"/api/workspaces/"+id, token, nil, &info)
	if err != nil {
		if ce := (*clierr.Error)(nil); asClierr(err, &ce) && ce.Code == "not_found" {
			return WorkspaceInfo{}, &clierr.Error{
				Code: "not_found", Field: "workspace",
				Message:    fmt.Sprintf("no workspace %q is registered", id),
				Suggestion: "list yours with `runko workspace list`, or create it with `runko workspace create`",
			}
		}
		return WorkspaceInfo{}, err
	}
	remoteURL, err := composeRemoteURL(runkodURL, token, info.RepoPath)
	if err != nil {
		return WorkspaceInfo{}, err
	}
	if err := ensureSharedClone(cloneDir, remoteURL); err != nil {
		return WorkspaceInfo{}, err
	}

	// Prefer the branch's snapshot tip; a branch that never snapshotted
	// restores at the workspace's base revision.
	if branch == "" {
		branch = "head"
	}
	snapshotRef := "refs/workspaces/" + id + "/" + branch
	startPoint := info.BaseRevision
	if _, err := runGit(cloneDir, "fetch", "origin", "+"+snapshotRef+":"+snapshotRef); err == nil {
		if sha, err := runGit(cloneDir, "rev-parse", snapshotRef); err == nil {
			startPoint = sha
		}
	}
	if err := materializeWorktree(cloneDir, dir, info, startPoint, branch); err != nil {
		return WorkspaceInfo{}, err
	}
	return info, nil
}

// WorkspaceSnapshot commits all WIP in dir (amend-by-default, §12.2) and
// force-pushes it to the workspace's snapshot ref - durability through the
// same receive funnel as Changes, so policy and secret scan run BEFORE the
// bytes become durable.
func WorkspaceSnapshot(dir, message string) (ref string, err error) {
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
	var identity []string
	if email, _ := runGit(dir, "config", "user.email"); email == "" {
		identity = []string{"-c", "user.name=Runko Workspace", "-c", "user.email=runko-workspace@localhost"}
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
	if _, err := runGit(dir, "push", "origin", "+HEAD:"+ref); err != nil {
		return "", fmt.Errorf("push snapshot: %w", err)
	}
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

// WorkspaceUpdateBase is §12.3's "sync base" row: fetch trunk, rebase the
// workspace onto its tip, and record the new base in the registry. On
// conflict the rebase is aborted and the conflicted paths are named -
// never a half-rebased tree (§6.6's conflict UX bar).
func WorkspaceUpdateBase(ctx context.Context, client *http.Client, runkodURL, token, dir string) (newBase string, err error) {
	id, err := runGit(dir, "config", "--worktree", "runko.workspace")
	if err != nil || id == "" {
		return "", &clierr.Error{
			Code: "not_a_workspace", Field: "dir",
			Message: fmt.Sprintf("%s is not a runko workspace worktree", dir),
		}
	}
	trunk, err := runGit(dir, "config", "--worktree", "runko.trunk")
	if err != nil || trunk == "" {
		trunk = "main"
	}
	if _, err := runGit(dir, "fetch", "origin", trunk); err != nil {
		return "", fmt.Errorf("fetch trunk: %w", err)
	}
	newBase, err = runGit(dir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return "", err
	}
	// Same identity fallback as WorkspaceSnapshot: rebase re-commits, so it
	// needs a committer even on an unconfigured machine.
	rebaseArgs := []string{"rebase", newBase}
	if email, _ := runGit(dir, "config", "user.email"); email == "" {
		rebaseArgs = append([]string{"-c", "user.name=Runko Workspace", "-c", "user.email=runko-workspace@localhost"}, rebaseArgs...)
	}
	if _, rebaseErr := runGit(dir, rebaseArgs...); rebaseErr != nil {
		conflicts, _ := runGit(dir, "diff", "--name-only", "--diff-filter=U")
		runGit(dir, "rebase", "--abort")
		if conflicts == "" {
			// Not a content conflict - surface the real failure, never a
			// misleading "conflicts in:" with an empty list.
			return "", fmt.Errorf("rebase onto %s: %w", short(newBase), rebaseErr)
		}
		return "", &clierr.Error{
			Code: "rebase_conflict", Field: "workspace",
			Message:    fmt.Sprintf("rebasing onto trunk tip %s conflicts in: %s", short(newBase), strings.ReplaceAll(conflicts, "\n", ", ")),
			Suggestion: "resolve by hand: git rebase " + short(newBase) + ", fix conflicts, then run update-base again",
		}
	}
	err = apiJSON(ctx, client, http.MethodPost,
		strings.TrimSuffix(runkodURL, "/")+"/api/workspaces/"+id+"/base", token,
		map[string]string{"base_revision": newBase}, nil)
	if err != nil {
		return "", fmt.Errorf("record new base in registry: %w", err)
	}
	return newBase, nil
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
