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

// DoctorReport is what `runko doctor` checks (§6.9): a new engineer's
// first-week onboarding should not require a wiki page.
type DoctorReport struct {
	RepoDir         string
	TrunkRef        string
	HasRemote       bool
	RemoteName      string
	RemoteURL       string
	HasChangeIDHook bool
	HooksDir        string
	GitVersion      string
	GitVersionOK    bool
	GitVersionError string // set when git --version itself couldn't be parsed
}

// RunDoctor inspects repoDir and returns a DoctorReport. It never fails hard
// on a missing remote or hook - those are exactly what the report surfaces.
// Not being a git repository at all IS a hard failure, and gets a structured
// clierr.Error (§6.5) rather than raw `git rev-parse` exit-128 text - the
// same resolve-or-explain treatment stage 9a already gave `project create`
// (cmd/runko/project.go's resolveBaseOrEmpty), extended here since `doctor`
// had the identical raw-passthrough gap.
func RunDoctor(repoDir, trunkRef string) (DoctorReport, error) {
	if _, err := runGit(repoDir, "rev-parse", "--git-dir"); err != nil {
		return DoctorReport{}, &clierr.Error{
			Code:       "not_a_repo",
			Field:      "repo",
			Message:    fmt.Sprintf("%s is not a git repository", repoDir),
			Suggestion: "run `git init` first, then retry `runko doctor`",
			DocURL:     "docs/design.md#67-empty-states-and-education",
		}
	}

	report := DoctorReport{RepoDir: repoDir, TrunkRef: trunkRef}

	hooksDir, err := hooksDirectory(repoDir)
	if err != nil {
		return DoctorReport{}, fmt.Errorf("doctor: resolve hooks directory: %w", err)
	}
	report.HooksDir = hooksDir
	report.HasChangeIDHook = hookInstalledAt(filepath.Join(hooksDir, "commit-msg"))

	if v, err := gitversion.Detect(); err != nil {
		report.GitVersionError = err.Error()
	} else {
		report.GitVersion = v.String()
		report.GitVersionOK = !v.Less(gitversion.Minimum)
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

	return report, nil
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

func hookInstalledAt(path string) bool {
	content, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(content), "Change-Id")
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

// PrintCheatSheet renders the report as the "personal cheat-sheet" §6.9 asks
// for: the three commands that matter, plus what needs fixing.
func PrintCheatSheet(w io.Writer, report DoctorReport) {
	fmt.Fprintln(w, "runko doctor")
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
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Three commands that matter:")
	fmt.Fprintln(w, "  runko change push                          # push HEAD for review")
	fmt.Fprintln(w, "  runko change requirements                  # owners + checks outstanding")
	fmt.Fprintf(w, "  git push origin HEAD:refs/for/%s          # same as `runko change push`, no CLI required\n", report.TrunkRef)
}
