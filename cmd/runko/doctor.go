package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// changeIDHookScript is a minimal commit-msg hook mirroring what the real
// server-side receive funnel enforces (receive.EnsureChangeID, §11.5) -
// client-side convenience, not a substitute for server-side enforcement,
// since client hooks are advisory only (§8.4, §15.3).
const changeIDHookScript = `#!/bin/sh
# Installed by ` + "`runko doctor --install-hook`" + ` (docs/design.md §6.9, §11.5).
# Appends a Change-Id trailer if the commit message doesn't already have one.
if ! grep -q '^Change-Id: I[0-9a-f]\{40\}$' "$1"; then
  id="I$(git var GIT_COMMITTER_IDENT | sha1sum | cut -c1-40)"
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
}

// RunDoctor inspects repoDir and returns a DoctorReport. It never fails hard
// on a missing remote or hook - those are exactly what the report surfaces.
func RunDoctor(repoDir, trunkRef string) (DoctorReport, error) {
	report := DoctorReport{RepoDir: repoDir, TrunkRef: trunkRef}

	hooksDir, err := hooksDirectory(repoDir)
	if err != nil {
		return DoctorReport{}, fmt.Errorf("doctor: resolve hooks directory: %w", err)
	}
	report.HooksDir = hooksDir
	report.HasChangeIDHook = hookInstalledAt(filepath.Join(hooksDir, "commit-msg"))

	if remotes, err := runGit(repoDir, "remote"); err == nil {
		if names := strings.Fields(remotes); len(names) > 0 {
			report.HasRemote = true
			report.RemoteName = names[0]
			if url, err := runGit(repoDir, "remote", "get-url", names[0]); err == nil {
				report.RemoteURL = url
			}
		}
	}

	return report, nil
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
