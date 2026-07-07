package runkod

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// EnsureBareRepo creates a bare repo at repoDir (with trunkRef as the
// initial branch) if one doesn't already exist there, and enables
// smart-HTTP push. Safe to call on an existing repo - a no-op past init.
func EnsureBareRepo(repoDir, trunkRef string) error {
	if _, err := os.Stat(filepath.Join(repoDir, "HEAD")); err == nil {
		return EnableHTTPReceivePack(repoDir)
	}
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return fmt.Errorf("runkod: create repo dir: %w", err)
	}
	cmd := exec.Command("git", "init", "-q", "--bare", "-b", trunkRef, repoDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("runkod: git init --bare: %w: %s", err, out)
	}
	return EnableHTTPReceivePack(repoDir)
}

// preReceiveHookScript renders the pre-receive hook installed into a served
// repo. It is a thin shim, not the policy logic itself (§7.4's enforcement
// lives in Processor, running inside the daemon process) - the hook process
// is a grandchild of the daemon (spawned by `git receive-pack`, itself
// spawned by `git http-backend`), so it cannot call Go methods on the
// daemon's in-memory Store directly; it forwards stdin to the daemon's
// internal endpoint over HTTP and relays the verdict.
//
// runkodBin is this daemon's own executable (resolved via os.Executable() at
// install time) invoked with a hidden `hook pre-receive` subcommand
// (cmd/runkod) - reusing the same binary avoids shipping/locating a second
// compiled shim.
func preReceiveHookScript(runkodBin, addr, token string) string {
	return fmt.Sprintf("#!/bin/sh\n"+
		"# Installed by runkod (docs/design.md §28.3 stage 10, §7.4, §11.5).\n"+
		"# Enforces the closed-trunk write path for every push, any transport.\n"+
		"exec %q hook pre-receive --addr %q --token %q\n",
		runkodBin, addr, token)
}

// InstallPreReceiveHook writes the pre-receive hook into repoDir/hooks,
// pointing back at this daemon (addr) with the given shared-secret token
// (checked by the /internal/pre-receive handler, api.go).
func InstallPreReceiveHook(repoDir, addr, token string) error {
	runkodBin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("runkod: resolve own executable path: %w", err)
	}
	hooksDir := filepath.Join(repoDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return fmt.Errorf("runkod: create hooks dir: %w", err)
	}
	path := filepath.Join(hooksDir, "pre-receive")
	script := preReceiveHookScript(runkodBin, addr, token)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		return fmt.Errorf("runkod: write pre-receive hook: %w", err)
	}
	return nil
}
