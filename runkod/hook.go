package runkod

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// EnsureBareRepo creates a bare repo at repoDir (with trunkRef as the
// initial branch) if one doesn't already exist there, and enables
// smart-HTTP push. Safe to call on an existing repo - a no-op past init.
func EnsureBareRepo(repoDir, trunkRef string) error {
	if _, err := os.Stat(filepath.Join(repoDir, "HEAD")); err == nil {
		if err := PruneDanglingChangeRefs(repoDir); err != nil {
			return err
		}
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

// PruneDanglingChangeRefs deletes any refs/changes/<id>/head that points at
// an object no longer present in the store. Such a ref is a repo-level
// hazard: git receive-pack's connectivity check fails the WHOLE repo when
// ANY ref is unreachable, so a single dangling change ref rejects EVERY
// push with "missing necessary objects" (and freezes the outbound mirror
// the same way). They arise when a push's pre-receive handler writes the
// change ref (referencing a still-quarantined object) and the daemon is
// then killed before git migrates quarantine to the object store - e.g. a
// `kubectl rollout restart` racing an in-flight push. Running this at boot
// makes that crash self-healing rather than a manual `git update-ref -d`
// in the pod (docs/migration-findings.md #34).
func PruneDanglingChangeRefs(repoDir string) error {
	out, err := exec.Command("git", "-C", repoDir,
		"for-each-ref", "--format=%(refname) %(objectname)", "refs/changes/").Output()
	if err != nil {
		return fmt.Errorf("runkod: list change refs: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		ref, sha, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		if exec.Command("git", "-C", repoDir, "cat-file", "-e", sha+"^{commit}").Run() == nil {
			continue // object present - a healthy ref
		}
		if out, err := exec.Command("git", "-C", repoDir, "update-ref", "-d", ref).CombinedOutput(); err != nil {
			return fmt.Errorf("runkod: prune dangling change ref %s: %w: %s", ref, err, out)
		}
		log.Printf("runkod: pruned dangling change ref %s (object %s missing - crash-recovery, migration-findings #34)", ref, sha)
	}
	return nil
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
