package main

import (
	"fmt"

	"github.com/saxocellphone/runko/affected"
	"github.com/saxocellphone/runko/core"
	"github.com/saxocellphone/runko/index"
	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
)

// Affected implements `runko-ci affected` (§14.6, §14.4.3): compute the
// affected project set for a base..head range, purely from the local repo.
// This needs no control-plane API call - projects and affected computation
// are pure functions of tree state (§13.3), which is exactly why CI can run
// this offline against its own checkout.
func Affected(repoDir, base, head string, rootInvalidationPatterns []string) (affected.Result, error) {
	store := gitstore.New(repoDir)

	indexed, err := index.Scan(store, core.Revision(head), nil)
	if err != nil {
		return affected.Result{}, clierr.WrapRevisionError(fmt.Errorf("scan projects at %s: %w", head, err), "--head", head)
	}
	projects := make([]affected.ProjectInfo, len(indexed))
	for i, p := range indexed {
		projects[i] = affected.ProjectInfo{
			Name: p.Name, Path: p.Path, DeclaredDependencies: p.DeclaredDependencies,
		}
	}

	changedPaths, err := diffPaths(repoDir, base, head)
	if err != nil {
		wrapped := fmt.Errorf("diff %s..%s: %w", base, head, err)
		return affected.Result{}, clierr.WrapRevisionErrorAmong(wrapped, map[string]string{"--base": base, "--head": head})
	}

	return affected.Compute(projects, changedPaths, affected.Options{
		RootInvalidationPatterns: rootInvalidationPatterns,
	}), nil
}
