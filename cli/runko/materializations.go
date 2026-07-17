// Local materialization lifecycle (§12.7): where workspace worktrees live
// on THIS machine, and the machine-local registry that tracks them.
//
// Placement: materializations default into one managed layout -
//
//	$RUNKO_WORKSPACE_HOME/                    (default ~/runko-ws)
//	  <host>/<org>/<repo>/.store/             the shared blobless clone
//	  <host>/<org>/<repo>/<workspace>/        one worktree per workspace
//	  <host>/<org>/<repo>/<workspace>@<br>/   parallel-branch attaches
//
// The registry ($XDG_STATE_HOME/runko/materializations.json) is §10.3's
// stance applied locally: a CACHE, never truth - truth is the worktrees
// themselves, each carrying worktree-scoped runko.* config, and
// `workspace gc --scan` rebuilds rows by walking a store's worktrees.
// Registry writes are therefore best-effort by design; the server never
// learns local paths.
package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
)

// Materialization is one registry row: a workspace worktree on this
// machine, and the store it hangs off.
type Materialization struct {
	Workspace  string    `json:"workspace"`
	Branch     string    `json:"branch"`
	Path       string    `json:"path"`
	Store      string    `json:"store"`
	RunkodURL  string    `json:"runkod_url"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

// materializationsPath honors XDG_STATE_HOME explicitly (the credential
// file's XDG lesson, auth.go), falling back to ~/.local/state.
func materializationsPath() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "runko", "materializations.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "runko", "materializations.json"), nil
}

func loadMaterializations() ([]Materialization, error) {
	p, err := materializationsPath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var rows []Materialization
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return rows, nil
}

func saveMaterializations(rows []Materialization) error {
	p, err := materializationsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// recordMaterialization upserts a row keyed by Path. Best-effort at every
// call site: the registry is a rebuildable cache, and a bookkeeping
// failure must never fail the verb that did the real work.
func recordMaterialization(m Materialization) error {
	rows, err := loadMaterializations()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	m.LastUsedAt = now
	for i := range rows {
		if rows[i].Path == m.Path {
			m.CreatedAt = rows[i].CreatedAt
			rows[i] = m
			return saveMaterializations(rows)
		}
	}
	m.CreatedAt = now
	return saveMaterializations(append(rows, m))
}

// touchMaterialization refreshes LastUsedAt for the worktree containing
// dir (gc's --idle signal). Silent best-effort, same stance as recording.
func touchMaterialization(dir string) {
	top, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return
	}
	rows, err := loadMaterializations()
	if err != nil {
		return
	}
	for i := range rows {
		if rows[i].Path == top {
			rows[i].LastUsedAt = time.Now().UTC()
			_ = saveMaterializations(rows)
			return
		}
	}
}

func dropMaterialization(p string) error {
	rows, err := loadMaterializations()
	if err != nil {
		return err
	}
	kept := rows[:0]
	for _, r := range rows {
		if r.Path != p {
			kept = append(kept, r)
		}
	}
	return saveMaterializations(kept)
}

// workspaceHome is the managed root: $RUNKO_WORKSPACE_HOME or ~/runko-ws.
func workspaceHome() (string, error) {
	if env := os.Getenv("RUNKO_WORKSPACE_HOME"); env != "" {
		return env, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "runko-ws"), nil
}

// managedPaths lays a workspace out under the managed home:
// <home>/<host>/<org>/<repo>/{.store, <workspace>[@<branch>]}. host comes
// from the control-plane URL (":" mangled for portability), org from its
// /o/<org> mount, repo from the served mount path the workspace API
// reports.
func managedPaths(runkodURL, repoPath, wsName, branch string) (store, dir string, err error) {
	home, err := workspaceHome()
	if err != nil {
		return "", "", err
	}
	u, err := url.Parse(runkodURL)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("parse runkod URL %q for the managed workspace home: %v", runkodURL, err)
	}
	host := strings.ReplaceAll(u.Host, ":", "_")
	org := "default"
	if _, after, ok := strings.Cut(u.Path, "/o/"); ok {
		if seg, _, _ := strings.Cut(after, "/"); seg != "" {
			org = seg
		}
	}
	repo := strings.TrimSuffix(path.Base("/"+repoPath), ".git")
	base := filepath.Join(home, host, org, repo)
	leaf := wsName
	if branch != "" && branch != "head" {
		leaf = wsName + "@" + branch
	}
	return filepath.Join(base, ".store"), filepath.Join(base, leaf), nil
}

// workspacePathLookup answers `runko workspace path [<name[@branch]>]`:
// scripting glue so "go to my workspace" is cd $(runko workspace path
// fix-x). With no name, the current checkout answers for itself (no
// registry needed); with one, the registry's freshest surviving row for
// that workspace (narrowed to @branch when given) wins.
func workspacePathLookup(name string) (Materialization, error) {
	if name == "" {
		id, err := runGit(".", "config", "runko.workspace")
		if err != nil || id == "" {
			return Materialization{}, &clierr.Error{
				Code: "not_a_workspace", Field: "dir",
				Message:    "the current directory is not a runko workspace worktree",
				Suggestion: "pass a workspace name: `runko workspace path <name>`",
			}
		}
		top, err := runGit(".", "rev-parse", "--show-toplevel")
		if err != nil {
			return Materialization{}, err
		}
		branch, _ := runGit(".", "config", "runko.branch")
		if branch == "" {
			branch = "head"
		}
		return Materialization{Workspace: id, Branch: branch, Path: top}, nil
	}
	ws, branch, _ := strings.Cut(name, "@")
	return materializationFor(ws, branch)
}

// materializationFor finds the registry's freshest SURVIVING row for a
// workspace; branch "" means any branch. Rows whose path is gone (rm -rf
// without gc) never match - the registry is a cache, the worktrees are
// truth.
func materializationFor(name, branch string) (Materialization, error) {
	rows, err := loadMaterializations()
	if err != nil {
		return Materialization{}, err
	}
	var best *Materialization
	for i := range rows {
		r := &rows[i]
		if r.Workspace != name || (branch != "" && r.Branch != branch) {
			continue
		}
		if _, err := os.Stat(r.Path); err != nil {
			continue
		}
		if best == nil || r.LastUsedAt.After(best.LastUsedAt) {
			best = r
		}
	}
	if best == nil {
		what := name
		attach := "runko workspace attach " + name
		if branch != "" {
			what = name + "@" + branch
			attach += " --branch " + branch
		}
		return Materialization{}, &clierr.Error{
			Code: "not_materialized", Field: "workspace",
			Message:    fmt.Sprintf("workspace %q has no materialization on this machine", what),
			Suggestion: "restore it with `" + attach + "`, or `runko workspace list` to see what's local",
		}
	}
	return *best, nil
}

// resolveWorkspaceDir makes the worktree transparent (§12.7): -w names a
// workspace (optionally name@branch) and the registry answers where it
// lives on this machine, so checkout verbs run from anywhere - the repo
// root included - without cd'ing into the materialization. An empty
// workspace falls through to dir, the historical stand-inside-it default;
// naming both is a contradiction and refused.
func resolveWorkspaceDir(workspace, dir string) (string, error) {
	if workspace == "" {
		return dir, nil
	}
	if dir != "" && dir != "." {
		return "", &clierr.Error{
			Code: "workspace_and_dir", Field: "workspace",
			Message:    "-w/--workspace and --dir/--repo both name a checkout - pick one",
			Suggestion: "drop --dir; -w resolves the workspace's own materialization from the registry",
		}
	}
	ws, branch, _ := strings.Cut(workspace, "@")
	m, err := materializationFor(ws, branch)
	if err != nil {
		return "", err
	}
	touchMaterialization(m.Path)
	return m.Path, nil
}

// localPathsByWorkspace maps workspace id -> surviving materialization
// paths on this machine (`workspace list`'s local column).
func localPathsByWorkspace() map[string][]string {
	rows, err := loadMaterializations()
	if err != nil {
		return nil
	}
	byWS := map[string][]string{}
	for _, r := range rows {
		if _, err := os.Stat(r.Path); err == nil {
			byWS[r.Workspace] = append(byWS[r.Workspace], r.Path)
		}
	}
	return byWS
}

// refuseNestedCheckout is §12.7's placement guard: materializing a
// workspace (or its store) inside another git working tree is how the
// in-tree sprawl happened - the host checkout's status filled with
// foreign worktrees and its build tooling walked into them (gazelle
// stripped BUILD deps through nested trees; migration finding #49).
// Checked from the deepest EXISTING ancestor, since the target itself is
// created by the materialization.
func refuseNestedCheckout(target string, force bool) error {
	if force {
		return nil
	}
	anc := filepath.Dir(target)
	for {
		if _, err := os.Stat(anc); err == nil {
			break
		}
		parent := filepath.Dir(anc)
		if parent == anc {
			return nil
		}
		anc = parent
	}
	inWorkTree, _ := runGit(anc, "rev-parse", "--is-inside-work-tree")
	inGitDir, _ := runGit(anc, "rev-parse", "--is-inside-git-dir")
	if inWorkTree != "true" && inGitDir != "true" {
		return nil
	}
	return &clierr.Error{
		Code: "workspace_nested_checkout", Field: "dir",
		Message:    fmt.Sprintf("%s is inside another git checkout (%s)", target, anc),
		Suggestion: "omit --dir/--clone-dir to use the managed home (~/runko-ws, override with RUNKO_WORKSPACE_HOME), or pass --force-nested if you really mean it",
	}
}
