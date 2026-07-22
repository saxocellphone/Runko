package main

// runko agent hooks --install tests (§12.6.1, decided 2026-07-14): the
// merge is conservative (foreign settings survive, unparseable refuses,
// twice installs once), and the installed file can NEVER ride into a
// snapshot - the load-bearing promise, proven against a real worktree
// whose repo has no gitignore of its own.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
)

func TestAgentHooksInstallCreatesSettingsAndExcludes(t *testing.T) {
	dir := newActivityWorkspaceDir(t, "obs")

	path, installed, excludedVia, err := InstallAgentHooks(dir)
	if err != nil {
		t.Fatalf("InstallAgentHooks: %v", err)
	}
	if !installed {
		t.Fatalf("expected a fresh install")
	}
	if excludedVia != "info/exclude" {
		t.Fatalf("a repo with no gitignore must exclude via info/exclude, got %q", excludedVia)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("installed settings are not valid JSON: %v", err)
	}
	if !strings.Contains(string(raw), agentHooksMarker) {
		t.Fatalf("settings missing the hook command, got:\n%s", raw)
	}

	// The whole point: the file is invisible to staging - git ignores it
	// and the worktree still reads clean.
	if _, err := runGit(dir, "check-ignore", "-q", agentHooksSettingsPath); err != nil {
		t.Fatalf("settings file should be ignored after install: %v", err)
	}
	if status := mustGit(t, dir, "status", "--porcelain"); status != "" {
		t.Fatalf("worktree should read clean after install, got:\n%s", status)
	}
}

func TestAgentHooksInstallMergesForeignSettings(t *testing.T) {
	dir := newActivityWorkspaceDir(t, "obs")
	seed := `{
  "permissions": {"allow": ["Bash(ls:*)"]},
  "hooks": {"PreToolUse": [{"matcher": "Bash", "hooks": [{"type": "command", "command": "echo hi"}]}]}
}`
	settings := filepath.Join(dir, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, installed, _, err := InstallAgentHooks(dir); err != nil || !installed {
		t.Fatalf("InstallAgentHooks: installed=%v err=%v", installed, err)
	}

	raw, _ := os.ReadFile(settings)
	var root struct {
		Permissions map[string]any   `json:"permissions"`
		Hooks       map[string][]any `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("merged settings are not valid JSON: %v", err)
	}
	if root.Permissions == nil {
		t.Fatalf("foreign permissions key must survive the merge, got:\n%s", raw)
	}
	if len(root.Hooks["PreToolUse"]) != 1 {
		t.Fatalf("foreign PreToolUse hook must survive the merge, got:\n%s", raw)
	}
	if len(root.Hooks["PostToolUse"]) != 1 {
		t.Fatalf("expected our PostToolUse entry appended, got:\n%s", raw)
	}
}

func TestAgentHooksInstallIsIdempotent(t *testing.T) {
	dir := newActivityWorkspaceDir(t, "obs")
	if _, installed, _, err := InstallAgentHooks(dir); err != nil || !installed {
		t.Fatalf("first install: installed=%v err=%v", installed, err)
	}
	path, installed, _, err := InstallAgentHooks(dir)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if installed {
		t.Fatalf("second install must be a no-op")
	}
	raw, _ := os.ReadFile(path)
	if n := strings.Count(string(raw), agentHooksMarker); n != 1 {
		t.Fatalf("expected exactly one wired hook after two installs, got %d:\n%s", n, raw)
	}
}

func TestAgentHooksInstallRefusesUnparseableSettings(t *testing.T) {
	for name, seed := range map[string]string{
		"garbage":          "{not json",
		"wrong hooks type": `{"hooks": "nope"}`,
		"wrong post type":  `{"hooks": {"PostToolUse": {"matcher": "x"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := newActivityWorkspaceDir(t, "obs")
			settings := filepath.Join(dir, ".claude", "settings.local.json")
			if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(settings, []byte(seed), 0o644); err != nil {
				t.Fatal(err)
			}

			_, _, _, err := InstallAgentHooks(dir)
			var ce *clierr.Error
			if !errors.As(err, &ce) || ce.Code != "invalid_settings" {
				t.Fatalf("expected invalid_settings, got %v", err)
			}
			raw, _ := os.ReadFile(settings)
			if string(raw) != seed {
				t.Fatalf("a refused file must be left byte-identical, got:\n%s", raw)
			}
		})
	}
}

func TestAgentHooksInstallOutsideWorkspaceIsStructured(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet")
	_, _, _, err := InstallAgentHooks(dir)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_a_workspace" {
		t.Fatalf("expected not_a_workspace, got %v", err)
	}
}

// TestInstalledSettingsNeverRideIntoSnapshots is the load-bearing promise:
// against a REAL worktree of a shared clone (whose repo has no gitignore
// covering .claude), the installed file stays out of the snapshot commit -
// the info/exclude mechanism, honored by snapshot staging.
func TestInstalledSettingsNeverRideIntoSnapshots(t *testing.T) {
	srv, bare := startWorkspaceServer(t)
	root := t.TempDir()
	wsDir := filepath.Join(root, "hooked")

	if _, _, err := WorkspaceCreate(context.Background(), http.DefaultClient, srv.URL, "sekret",
		"hooked", "alice", []string{"checkout-api"}, nil, MaterializeOptions{CloneDir: filepath.Join(root, "mono"), Dir: wsDir}); err != nil {
		t.Fatalf("WorkspaceCreate: %v", err)
	}
	if _, installed, via, err := InstallAgentHooks(wsDir); err != nil || !installed || via != "info/exclude" {
		t.Fatalf("InstallAgentHooks: installed=%v via=%q err=%v", installed, via, err)
	}

	writeFile(t, wsDir, "commerce/checkout/wip.go", "package main // wip\n")
	ref, err := WorkspaceSnapshot(wsDir, "wip with hooks installed")
	if err != nil {
		t.Fatalf("WorkspaceSnapshot: %v", err)
	}
	tree := mustGit(t, bare, "ls-tree", "-r", "--name-only", ref)
	if !strings.Contains(tree, "commerce/checkout/wip.go") {
		t.Fatalf("snapshot should carry the WIP, got:\n%s", tree)
	}
	if strings.Contains(tree, "settings.local.json") {
		t.Fatalf("harness settings must NEVER ride into a snapshot, got:\n%s", tree)
	}
}

func TestDoctorReportsAgentHooksInsideWorkspaces(t *testing.T) {
	dir := newActivityWorkspaceDir(t, "obs")

	report, err := RunDoctor(dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor: %v", err)
	}
	if report.WorkspaceID != "obs" || report.HasAgentHooks {
		t.Fatalf("expected bound workspace without hooks, got %+v", report)
	}
	var sheet strings.Builder
	PrintCheatSheet(&sheet, report)
	if !strings.Contains(sheet.String(), "runko agent hooks --install") {
		t.Fatalf("cheat-sheet should name the install verb when hooks are missing:\n%s", sheet.String())
	}

	if _, _, _, err := InstallAgentHooks(dir); err != nil {
		t.Fatalf("InstallAgentHooks: %v", err)
	}
	report, err = RunDoctor(dir, "main")
	if err != nil {
		t.Fatalf("RunDoctor after install: %v", err)
	}
	if !report.HasAgentHooks {
		t.Fatalf("expected HasAgentHooks after install, got %+v", report)
	}

	// Outside a workspace: no binding, no nag.
	plain := t.TempDir()
	mustGit(t, plain, "init", "--quiet")
	report, err = RunDoctor(plain, "main")
	if err != nil {
		t.Fatalf("RunDoctor (plain repo): %v", err)
	}
	if report.WorkspaceID != "" {
		t.Fatalf("plain repo must not report a workspace, got %+v", report)
	}
	sheet.Reset()
	PrintCheatSheet(&sheet, report)
	if strings.Contains(sheet.String(), "agent hooks:") {
		t.Fatalf("cheat-sheet must not mention agent hooks outside a workspace:\n%s", sheet.String())
	}
}

func TestWorkspaceStreamingGuidanceNamesBothVerbs(t *testing.T) {
	var out strings.Builder
	printWorkspaceStreamingGuidance(&out, "payments-fix")
	for _, want := range []string{
		"runko workspace watch -w payments-fix",
		"runko agent hooks --install -w payments-fix",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("guidance missing %q:\n%s", want, out.String())
		}
	}
}

// The worktree is materialization detail (§12.7): every command these two
// teach addresses the workspace by name, so neither may hand out a cd -
// that is the habit -w exists to delete, and printing one trains it right
// where a workspace is born.
//
// The assertions are deliberately whole-output rather than "no cd
// anywhere": a printer that regressed to prose, or to some other command
// style, would sail past a substring check while teaching the wrong thing.
// Every non-header line must BE a runko command that names the workspace,
// and each block must still print the number of commands it is meant to.
func TestWorkspaceGuidanceNeverTeachesCd(t *testing.T) {
	for _, tc := range []struct {
		name     string
		print    func(*strings.Builder)
		wantCmds int
	}{
		{"streaming", func(b *strings.Builder) { printWorkspaceStreamingGuidance(b, "payments-fix") }, 2},
		{"loop", func(b *strings.Builder) { printWorkspaceLoop(b, "payments-fix") }, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			tc.print(&out)

			cmds := 0
			for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasSuffix(line, ":") {
					continue // the prose header introducing the block
				}
				cmds++
				if !strings.HasPrefix(line, "runko ") {
					t.Errorf("not a runko command (a shell escape hatch would slip the -w check): %q", line)
					continue
				}
				if !strings.Contains(line, "-w payments-fix") {
					t.Errorf("command does not name the workspace: %q", line)
				}
			}
			if cmds != tc.wantCmds {
				t.Errorf("printed %d commands, want %d - a printer that stops printing "+
					"cannot teach the wrong thing, but it cannot teach the right thing either:\n%s",
					cmds, tc.wantCmds, out.String())
			}
			// Belt and braces: no cd in any form, including the ones the
			// line-by-line rule above would not catch on a prose line.
			for _, forbidden := range []string{"cd ", "cd\t", "pushd", "$(runko workspace path"} {
				if strings.Contains(out.String(), forbidden) {
					t.Errorf("guidance sends the reader into the worktree (%q):\n%s", forbidden, out.String())
				}
			}
		})
	}
}

// A plain attach is on "head" and addresses the workspace by bare name;
// only a non-default branch earns the @branch suffix. The `--branch` flag
// defaults to "head" rather than "", so an emptiness check alone would
// teach every attach a pointless @head - this pins both.
func TestWorkspaceHandleOmitsDefaultBranch(t *testing.T) {
	for _, tc := range []struct{ branch, want string }{
		{"", "payments-fix"},
		{"head", "payments-fix"},
		{"experiment", "payments-fix@experiment"},
	} {
		if got := workspaceHandle("payments-fix", tc.branch); got != tc.want {
			t.Errorf("workspaceHandle(payments-fix, %q) = %q, want %q", tc.branch, got, tc.want)
		}
	}
}
