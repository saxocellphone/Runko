package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitversion"
)

// changeIDHookScript is a minimal commit-msg hook mirroring what the real
// server-side receive funnel enforces (receive.EnsureChangeID, §11.5) -
// client-side convenience, not a substitute for server-side enforcement,
// since client hooks are advisory only (§8.4, §15.3).
// The seed mixes what Gerrit's own hook mixes - tree, parents, author and
// committer idents, the message itself - PLUS random bytes: a seed built
// from identity alone hands every commit from the same person the same id,
// collapsing distinct work into one Change on push. Hashing goes through
// `git hash-object --stdin` (guaranteed present in a git hook; `sha1sum`
// isn't on macOS), and cut -c1-40 keeps the id shape stable even in
// sha256-object-format repos.
const changeIDHookScript = `#!/bin/sh
# Installed by ` + "`runko doctor --install-hook`" + ` (docs/design.md §6.9, §11.5).
# Appends a Change-Id trailer if the commit message doesn't already have one.
if ! grep -q '^Change-Id: I[0-9a-f]\{40\}$' "$1"; then
  random=$(od -An -tx1 -N16 /dev/urandom 2>/dev/null | tr -d ' \n')
  [ -n "$random" ] || random="$$-$(date +%s)"
  id="I$({
    git write-tree 2>/dev/null
    git rev-parse HEAD 2>/dev/null
    git var GIT_AUTHOR_IDENT 2>/dev/null
    git var GIT_COMMITTER_IDENT 2>/dev/null
    cat "$1"
    echo "$random"
  } | git hash-object --stdin | cut -c1-40)"
  printf '\nChange-Id: %s\n' "$id" >> "$1"
fi
`

// verbNudgeHookScript is an advisory pre-commit hook: when a raw
// `git commit` runs in a Runko-managed checkout, it prints the native
// verbs to stderr and ALWAYS exits 0 - plain git is a contract, not a
// fallback (§6.9's parity rule), so the nudge must never block. The §6.9
// rejection UX applied one moment earlier: agents (and humans) read the
// exact next command at the moment they reach for muscle-memory git,
// instead of first meeting it as a pre-receive rejection at push time.
// The verbs are the runko CLI's in EVERY checkout - jj colocated included
// (§21, repositioned 2026-07-11: runko is the primary interface for basic
// operations; jj is the surgical tool) - so a colocated checkout gets the
// same verbs plus a note on where jj now fits.
// Two silences are load-bearing: runko's own verbs run git with
// RUNKO_INTERNAL_GIT=1 (git.go) so `change create`/`workspace snapshot`
// don't nudge about themselves, and jj runs no git hooks at all, so jj
// surgery never sees it - it fires precisely and only on a raw git commit.
const verbNudgeHookScript = `#!/bin/sh
# runko-verb-nudge: advisory, installed by runko (docs/design.md §6.9, §21).
# Prints the native verbs on a raw git commit; never blocks (always exit 0).
[ -n "$RUNKO_INTERNAL_GIT" ] && exit 0
cat >&2 <<'MSG'
runko: raw git commit works, but this checkout has native verbs:
  runko change create -m "<msg>"   # commit all work, Change-Id stamped
  runko change push                # submit the stack for review
MSG
if [ -d "$(git rev-parse --show-toplevel 2>/dev/null)/.jj" ]; then
  echo '(jj here is for surgery - jj edit/squash/split; the basic loop is runko)' >&2
else
  echo '(runko doctor prints the full cheat-sheet)' >&2
fi
exit 0
`

// verbNudgeMarker identifies OUR pre-commit hook, so installs are
// idempotent but a foreign pre-commit hook is never clobbered.
const verbNudgeMarker = "runko-verb-nudge"

// DoctorReport is what `runko doctor` checks (§6.9): a new engineer's
// first-week onboarding should not require a wiki page.
type DoctorReport struct {
	RepoDir         string
	TrunkRef        string
	HasRemote       bool
	RemoteName      string
	RemoteURL       string
	HasChangeIDHook bool
	// HasVerbNudgeHook: the advisory pre-commit hook that answers a raw
	// `git commit` with the native verbs (never blocking - §6.9).
	HasVerbNudgeHook bool
	HooksDir         string
	GitVersion       string
	GitVersionOK     bool
	GitVersionError  string // set when git --version itself couldn't be parsed
	// jj-first client (§7.4, decided 2026-07-08): a colocated jj workspace
	// is the intended daily driver - amend anywhere in a stack, jj
	// auto-rebases descendants, one push updates every Change (the funnel's
	// series processing). Identity comes from jj's trailer template, not
	// the commit-msg hook.
	IsJJWorkspace    bool
	JJChangeIDsWired bool
	// WorkspaceID: the runko.workspace binding, when this checkout is a
	// workspace worktree. HasAgentHooks: whether the §12.6.1 activity
	// hooks are wired in the worktree's harness settings (`runko agent
	// hooks --install`) - only meaningful (and only reported) when
	// WorkspaceID is set.
	WorkspaceID   string
	HasAgentHooks bool
	// TrackedMaterializations: §12.7's machine-local registry rows whose
	// directories still exist - `runko workspace gc` reviews and reclaims
	// them. Machine state, not repo state; reported so the cheat sheet
	// can point at the chore verb without a network call.
	TrackedMaterializations int
	// CLI is this binary's own build identity (version.go) - reported so
	// "which runko bit us" is answerable from any doctor paste (2026-07-16
	// dogfood review: version drift had no verb).
	CLI BuildIdentity
}

// RunDoctor inspects repoDir and returns a DoctorReport. It never fails hard
// on a missing remote or hook - those are exactly what the report surfaces.
// Not being a git repository at all IS a hard failure, and gets a structured
// clierr.Error (§6.5) rather than raw `git rev-parse` exit-128 text - the
// same resolve-or-explain treatment stage 9a already gave `project create`
// (cli/runko/project.go's resolveBaseOrEmpty), extended here since `doctor`
// had the identical raw-passthrough gap.
func RunDoctor(repoDir, trunkRef string) (DoctorReport, error) {
	if _, err := runGit(repoDir, "rev-parse", "--git-dir"); err != nil {
		return DoctorReport{}, &clierr.Error{
			Code:       "not_a_repo",
			Field:      "repo",
			Message:    fmt.Sprintf("%s is not a git repository", repoDir),
			Suggestion: "run `git init` (or `jj git init --colocate`) first, then retry `runko doctor`",
			DocURL:     "docs/design.md#67-empty-states-and-education",
		}
	}

	report := DoctorReport{RepoDir: repoDir, TrunkRef: trunkRef, CLI: buildIdentity()}

	hooksDir, err := hooksDirectory(repoDir)
	if err != nil {
		return DoctorReport{}, fmt.Errorf("doctor: resolve hooks directory: %w", err)
	}
	report.HooksDir = hooksDir
	report.HasChangeIDHook = hookContains(filepath.Join(hooksDir, "commit-msg"), "Change-Id")
	report.HasVerbNudgeHook = hookContains(filepath.Join(hooksDir, "pre-commit"), verbNudgeMarker)

	if v, err := gitversion.Detect(); err != nil {
		report.GitVersionError = err.Error()
	} else {
		report.GitVersion = v.String()
		report.GitVersionOK = !v.Less(gitversion.Minimum)
	}

	if isJJWorkspace(repoDir) {
		report.IsJJWorkspace = true
		report.JJChangeIDsWired = jjTrailerConfigured(repoDir)
	}

	if id, _ := runGit(repoDir, "config", "runko.workspace"); id != "" {
		report.WorkspaceID = id
		if top, err := runGit(repoDir, "rev-parse", "--show-toplevel"); err == nil {
			report.HasAgentHooks = hookContains(filepath.Join(top, ".claude", "settings.local.json"), agentHooksMarker) ||
				hookContains(filepath.Join(top, ".claude", "settings.json"), agentHooksMarker)
		}
	}

	if remotes, err := runGit(repoDir, "remote"); err == nil {
		if names := strings.Fields(remotes); len(names) > 0 {
			report.HasRemote = true
			report.RemoteName = names[0]
			if url, err := runGit(repoDir, "remote", "get-url", names[0]); err == nil {
				// Redacted at report-build time so --json can't leak an
				// embedded smart-HTTP credential either (remote URLs carry
				// user:pass here - Credential.GitUserPass's documented form).
				report.RemoteURL = redactURLPassword(url)
			}
		}
	}

	report.TrackedMaterializations = len(localPathsByWorkspaceFlat())

	return report, nil
}

// localPathsByWorkspaceFlat flattens the registry to surviving paths.
func localPathsByWorkspaceFlat() []string {
	var paths []string
	for _, ps := range localPathsByWorkspace() {
		paths = append(paths, ps...)
	}
	return paths
}

// redactURLPassword replaces the password of a URL's user:pass@host
// userinfo with "***". Non-URL remotes (scp-style ssh paths) pass through
// unchanged - they don't embed passwords.
func redactURLPassword(remote string) string {
	u, err := url.Parse(remote)
	if err != nil || u.User == nil {
		return remote
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return remote
	}
	redacted := *u
	redacted.User = url.UserPassword(u.User.Username(), "***")
	// url.String() percent-encodes the literal password "***" as "%2A%2A%2A";
	// unescape just that so humans read stars, not percent soup.
	return strings.Replace(redacted.String(), "%2A%2A%2A", "***", 1)
}

func hooksDirectory(repoDir string) (string, error) {
	if custom, err := runGit(repoDir, "config", "--get", "core.hooksPath"); err == nil && custom != "" {
		if filepath.IsAbs(custom) {
			return custom, nil
		}
		return filepath.Join(repoDir, custom), nil
	}
	gitDir, err := runGit(repoDir, "rev-parse", "--git-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoDir, gitDir)
	}
	return filepath.Join(gitDir, "hooks"), nil
}

func hookContains(path, marker string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(content), marker)
}

// InstallChangeIDHook writes changeIDHookScript to repoDir's commit-msg hook.
func InstallChangeIDHook(repoDir string) error {
	hooksDir, err := hooksDirectory(repoDir)
	if err != nil {
		return fmt.Errorf("doctor: resolve hooks directory: %w", err)
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("doctor: create hooks directory: %w", err)
	}
	path := filepath.Join(hooksDir, "commit-msg")
	if err := os.WriteFile(path, []byte(changeIDHookScript), 0o755); err != nil {
		return fmt.Errorf("doctor: write commit-msg hook: %w", err)
	}
	return nil
}

// InstallChangeIDHookIfAbsent writes the commit-msg hook only when the repo
// has NO commit-msg hook at all - the implicit installers' rule (§6.10:
// workspace materialization wires the checkout without being asked). An
// existing hook is never touched, ours included: an explicit
// `runko doctor --install-hook` refreshes wording, an implicit path must
// not surprise - and a foreign commit-msg hook may be Gerrit's own, which
// also mints Change-Ids and must win.
func InstallChangeIDHookIfAbsent(repoDir string) (installed bool, err error) {
	hooksDir, err := hooksDirectory(repoDir)
	if err != nil {
		return false, fmt.Errorf("doctor: resolve hooks directory: %w", err)
	}
	if _, statErr := os.Stat(filepath.Join(hooksDir, "commit-msg")); statErr == nil {
		return false, nil
	}
	return true, InstallChangeIDHook(repoDir)
}

// InstallVerbNudgeHook writes verbNudgeHookScript to repoDir's pre-commit
// hook. A pre-commit hook we didn't write is left alone (installed=false):
// the nudge is advisory sugar, never worth clobbering a real hook - the
// same refusal SetupJJChangeIDs applies to a foreign trailers template.
// Re-installing over our own is fine (idempotent, picks up new wording).
func InstallVerbNudgeHook(repoDir string) (installed bool, err error) {
	hooksDir, err := hooksDirectory(repoDir)
	if err != nil {
		return false, fmt.Errorf("doctor: resolve hooks directory: %w", err)
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return false, fmt.Errorf("doctor: create hooks directory: %w", err)
	}
	path := filepath.Join(hooksDir, "pre-commit")
	if _, statErr := os.Stat(path); statErr == nil && !hookContains(path, verbNudgeMarker) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(verbNudgeHookScript), 0o755); err != nil {
		return false, fmt.Errorf("doctor: write pre-commit hook: %w", err)
	}
	return true, nil
}

// PrintCheatSheet renders the report as the "personal cheat-sheet" §6.9 asks
// for: the three commands that matter, plus what needs fixing.
func PrintCheatSheet(w io.Writer, report DoctorReport) {
	fmt.Fprintln(w, "runko doctor")
	fmt.Fprintf(w, "  runko cli:       %s\n", report.CLI)
	switch {
	case report.GitVersionError != "":
		fmt.Fprintf(w, "  git version:     could not detect (%s)\n", report.GitVersionError)
	case !report.GitVersionOK:
		fmt.Fprintf(w, "  git version:     %s - too old, need >= %s (`git merge-tree --merge-base`)\n", report.GitVersion, gitversion.Minimum)
	default:
		fmt.Fprintf(w, "  git version:     %s (OK)\n", report.GitVersion)
	}
	if report.HasRemote {
		fmt.Fprintf(w, "  remote:          %s -> %s\n", report.RemoteName, report.RemoteURL)
	} else {
		fmt.Fprintln(w, "  remote:          none configured")
	}
	if report.HasChangeIDHook {
		fmt.Fprintln(w, "  commit-msg hook: installed (adds Change-Id trailers)")
	} else {
		fmt.Fprintln(w, "  commit-msg hook: NOT installed - run `runko doctor --install-hook`")
	}
	if report.HasVerbNudgeHook {
		fmt.Fprintln(w, "  pre-commit hook: installed (nudges raw `git commit` toward the native verbs)")
	} else {
		fmt.Fprintln(w, "  pre-commit hook: NOT installed - run `runko doctor --install-hook`")
	}
	if report.IsJJWorkspace {
		if report.JJChangeIDsWired {
			fmt.Fprintln(w, "  jj workspace:    detected; Change-Id trailers derive from jj change ids")
		} else {
			fmt.Fprintln(w, "  jj workspace:    detected, but Change-Ids are NOT wired - run `runko doctor --install-hook`")
		}
	}
	// Streaming status is workspace-scoped by nature - outside a
	// workspace worktree there is nothing to stream into, so no nag.
	if report.WorkspaceID != "" {
		if report.HasAgentHooks {
			fmt.Fprintln(w, "  agent hooks:     installed (activity streams to the workspace page, §12.6.1)")
		} else {
			fmt.Fprintln(w, "  agent hooks:     NOT installed - run `runko agent hooks --install` (and keep `runko workspace watch` running)")
		}
	}
	if report.TrackedMaterializations > 0 {
		fmt.Fprintf(w, "  materializations: %d tracked on this machine - `runko workspace gc` reviews and reclaims (§12.7)\n", report.TrackedMaterializations)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The commands that matter:")
	fmt.Fprintln(w, "  runko change create -m \"...\"               # commit all work as one Change (stack by repeating)")
	fmt.Fprintln(w, "  runko change push                          # push your stack's tip for review")
	fmt.Fprintln(w, "  runko change requirements                  # owners + checks outstanding")
	fmt.Fprintf(w, "  git push origin HEAD:refs/for/%s          # same as `runko change push`, no CLI required\n", report.TrunkRef)
	if report.IsJJWorkspace {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "jj (surgical use - the basic loop above is runko everywhere, this checkout included):")
		fmt.Fprintln(w, "  jj edit <rev>  /  jj squash                 # rework ANY change mid-stack - jj restacks descendants")
		fmt.Fprintln(w, "  jj split                                    # carve an oversized change into reviewable steps")
		fmt.Fprintln(w, "  runko change push                           # one push; every Change in the stack updates")
	}
}
