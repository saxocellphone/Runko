package main

import "fmt"

// Checkout implements `runko-ci checkout` (§14.4.4, §14.6): partial-clone
// remote (never fetching blob content outside what's needed) into destDir,
// set a cone-mode sparse-checkout limited to projectPaths, then check out
// rev. This is the "checkout action" behavior §14.4.4 calls "as important as
// the Checks API - slow full clones will kill monorepo CI adoption."
func Checkout(remote, destDir, rev string, projectPaths []string) error {
	if _, err := runGit("", "clone", "--filter=blob:none", "--no-checkout", "--quiet", remote, destDir); err != nil {
		return fmt.Errorf("partial clone: %w", err)
	}
	if _, err := runGit(destDir, "sparse-checkout", "init", "--cone"); err != nil {
		return fmt.Errorf("sparse-checkout init: %w", err)
	}
	if len(projectPaths) > 0 {
		args := append([]string{"sparse-checkout", "set"}, projectPaths...)
		if _, err := runGit(destDir, args...); err != nil {
			return fmt.Errorf("sparse-checkout set: %w", err)
		}
	}
	if _, err := runGit(destDir, "checkout", "--quiet", rev); err != nil {
		return fmt.Errorf("checkout %s: %w", rev, err)
	}
	return nil
}
