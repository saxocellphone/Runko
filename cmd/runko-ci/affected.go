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
		// Match against the raw git error BEFORE adding the "diff %s..%s"
		// context below - that prefix mentions both base and head verbatim,
		// which would make WrapRevisionErrorAmong's substring match pick
		// whichever candidate a map iteration happened to visit first,
		// rather than the one git's own message actually names.
		if wrapped := clierr.WrapRevisionErrorAmong(err, map[string]string{"--base": base, "--head": head}); wrapped != err {
			return affected.Result{}, wrapped
		}
		return affected.Result{}, fmt.Errorf("diff %s..%s: %w", base, head, err)
	}

	return affected.Compute(projects, changedPaths, affected.Options{
		RootInvalidationPatterns: rootInvalidationPatterns,
	}), nil
}
