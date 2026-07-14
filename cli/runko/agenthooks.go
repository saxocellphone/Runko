// runko agent hooks --install - the §12.6.1 one-command activity wiring.
// Plain `agent hooks` stays the harness-agnostic snippet printer; --install
// merges that same snippet into the worktree's Claude Code
// .claude/settings.local.json (decided 2026-07-14: explicit opt-in per
// invocation, client-specific config is never written automatically). The
// merge is deliberately conservative - foreign keys survive, an already
// wired file no-ops, and an unparseable one is REFUSED rather than
// rewritten (the verb-nudge non-clobber posture, doctor.go). The installed
// file is then excluded from snapshots: `change create`/snapshot staging
// honor gitignore/exclude, and the shared clone's info/exclude covers
// every worktree (git keeps info/ in the common dir).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
)

// agentHooksMarker identifies OUR PostToolUse entry inside a settings file,
// so installs are idempotent but foreign hooks are never disturbed - the
// hookContains pattern (doctor.go).
const agentHooksMarker = "runko agent event --from-hook"

// agentHooksSettingsPath is where --install merges the snippet, relative to
// the worktree root (the Claude Code local-settings location; other
// harnesses take the printed snippet instead).
const agentHooksSettingsPath = ".claude/settings.local.json"

// InstallAgentHooks merges agentHooksSnippet into dir's worktree settings
// file and guarantees the file can never ride into a snapshot or change.
// installed=false means the hook was already wired (the exclusion is still
// verified). excludedVia reports which mechanism keeps it out of snapshots:
// "gitignore" (the repo's own ignore rules already cover it) or
// "info/exclude" (appended to the shared clone's exclude file).
func InstallAgentHooks(dir string) (path string, installed bool, excludedVia string, err error) {
	id, _ := runGit(dir, "config", "runko.workspace")
	if id == "" {
		return "", false, "", &clierr.Error{
			Code: "not_a_workspace", Field: "dir",
			Message:    fmt.Sprintf("%s is not bound to a runko workspace", dir),
			Suggestion: "run inside a `runko workspace create/attach` worktree, or bind a clone with `git config runko.workspace <id>`",
		}
	}
	top, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false, "", fmt.Errorf("agent hooks: resolve worktree root: %w", err)
	}
	path = filepath.Join(top, filepath.FromSlash(agentHooksSettingsPath))

	installed, err = mergeAgentHooksSettings(path)
	if err != nil {
		return "", false, "", err
	}
	excludedVia, err = ensureSnapshotExcluded(dir)
	if err != nil {
		return "", false, "", err
	}
	return path, installed, excludedVia, nil
}

// mergeAgentHooksSettings writes or merges the PostToolUse entry into the
// settings file at path. The snippet itself is the single source of truth -
// the entry is unmarshalled out of agentHooksSnippet, never duplicated.
func mergeAgentHooksSettings(path string) (installed bool, err error) {
	var snippet map[string]any
	if err := json.Unmarshal([]byte(agentHooksSnippet), &snippet); err != nil {
		return false, fmt.Errorf("agent hooks: snippet is not valid JSON (a bug): %w", err)
	}
	entry := snippet["hooks"].(map[string]any)["PostToolUse"].([]any)[0]

	raw, readErr := os.ReadFile(path)
	if readErr != nil && !os.IsNotExist(readErr) {
		return false, fmt.Errorf("agent hooks: read %s: %w", path, readErr)
	}

	root := map[string]any{}
	if readErr == nil {
		if err := json.Unmarshal(raw, &root); err != nil {
			return false, invalidSettingsErr(path)
		}
		if strings.Contains(string(raw), agentHooksMarker) {
			return false, nil // already wired - never write twice
		}
	}

	hooks, ok := root["hooks"].(map[string]any)
	if root["hooks"] != nil && !ok {
		return false, invalidSettingsErr(path)
	}
	if hooks == nil {
		hooks = map[string]any{}
	}
	post, ok := hooks["PostToolUse"].([]any)
	if hooks["PostToolUse"] != nil && !ok {
		return false, invalidSettingsErr(path)
	}
	hooks["PostToolUse"] = append(post, entry)
	root["hooks"] = hooks

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("agent hooks: encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("agent hooks: %w", err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return false, fmt.Errorf("agent hooks: write %s: %w", path, err)
	}
	return true, nil
}

func invalidSettingsErr(path string) error {
	return &clierr.Error{
		Code: "invalid_settings", Field: "settings",
		Message:    fmt.Sprintf("%s is not valid JSON (or its hooks key has an unexpected shape) - refusing to rewrite it", path),
		Suggestion: "fix or remove the file, or merge the `runko agent hooks` snippet in by hand",
	}
}

// ensureSnapshotExcluded guarantees the settings file never enters a
// snapshot or change: snapshot staging honors ignore rules, so either the
// repo's own gitignore already covers it, or the path is appended to the
// shared clone's info/exclude - which git keeps in the COMMON dir, so one
// append covers every worktree of the clone.
func ensureSnapshotExcluded(dir string) (via string, err error) {
	if _, err := runGit(dir, "check-ignore", "-q", agentHooksSettingsPath); err == nil {
		return "gitignore", nil
	}
	commonDir, err := runGit(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("agent hooks: resolve git common dir: %w", err)
	}
	excludePath := filepath.Join(commonDir, "info", "exclude")
	existing, readErr := os.ReadFile(excludePath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return "", fmt.Errorf("agent hooks: read %s: %w", excludePath, readErr)
	}
	if !strings.Contains(string(existing), agentHooksSettingsPath) {
		if err := os.MkdirAll(filepath.Dir(excludePath), 0o755); err != nil {
			return "", fmt.Errorf("agent hooks: %w", err)
		}
		line := "# runko agent hooks --install: harness config stays out of snapshots (§12.6.1)\n" +
			agentHooksSettingsPath + "\n"
		f, err := os.OpenFile(excludePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return "", fmt.Errorf("agent hooks: %w", err)
		}
		if _, err := f.WriteString(line); err != nil {
			f.Close()
			return "", fmt.Errorf("agent hooks: append %s: %w", excludePath, err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("agent hooks: %w", err)
		}
	}
	// Re-verify: a core.excludesFile override or negated pattern could
	// still win. Warn rather than fail - the install itself succeeded.
	if _, err := runGit(dir, "check-ignore", "-q", agentHooksSettingsPath); err != nil {
		fmt.Fprintf(warnWriter, "warning: %s is still not ignored after appending to %s - check core.excludesFile / negated patterns, or add it to .gitignore\n",
			agentHooksSettingsPath, excludePath)
	}
	return "info/exclude", nil
}
