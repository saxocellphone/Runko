package main

import (
	"context"
	"fmt"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/affected"
	"github.com/saxocellphone/runko/platform/buildadapter"
	"github.com/saxocellphone/runko/platform/buildadapter/bazel"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/index"
)

// AffectedOutput is what `runko-ci affected` prints: the platform-floor
// affected.Result, plus BuildRefinement when --engine was passed
// (docs/spec/build-adapter/README.md §2). BuildRefinement is additive
// information, never a replacement for Result - an org's check-set policies
// choosing to key on refined targets instead of projects is a policy
// decision made elsewhere, not something this CLI decides.
type AffectedOutput struct {
	affected.Result
	BuildRefinement *buildadapter.Refinement `json:"build_refinement,omitempty"`
}

// knownEngines maps --engine names to their buildadapter.Engine
// implementation (docs/spec/build-adapter/README.md §4's engine matrix).
var knownEngines = map[string]buildadapter.Engine{
	"bazel": bazel.Engine{},
}

// Affected implements `runko-ci affected` (§14.6, §14.4.3): compute the
// affected project set for a base..head range, purely from the local repo.
// This needs no control-plane API call - projects and affected computation
// are pure functions of tree state (§13.3), which is exactly why CI can run
// this offline against its own checkout. When engineName is non-empty, the
// platform-floor result is additionally refined via the named build-graph
// adapter (§14.5.4) - any engine failure escalates RunEverything for the
// WHOLE output, not just its own BuildRefinement sub-field, mirroring how a
// root-invalidation pattern already escalates affected.Result today.
func Affected(repoDir, base, head string, rootInvalidationPatterns []string, engineName, universePattern string, engineTimeout time.Duration) (AffectedOutput, error) {
	store := gitstore.New(repoDir)

	indexed, err := index.Scan(store, core.Revision(head), nil)
	if err != nil {
		return AffectedOutput{}, clierr.WrapRevisionError(fmt.Errorf("scan projects at %s: %w", head, err), "--head", head)
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
			return AffectedOutput{}, wrapped
		}
		return AffectedOutput{}, fmt.Errorf("diff %s..%s: %w", base, head, err)
	}

	result := affected.Compute(projects, changedPaths, affected.Options{
		RootInvalidationPatterns: rootInvalidationPatterns,
	})
	out := AffectedOutput{Result: result}

	if engineName == "" {
		return out, nil
	}
	engine, ok := knownEngines[engineName]
	if !ok {
		return AffectedOutput{}, &clierr.Error{
			Code:       "unknown_engine",
			Field:      "--engine",
			Message:    fmt.Sprintf("unknown build-graph engine %q", engineName),
			Suggestion: "supported engines: bazel",
			DocURL:     "docs/spec/build-adapter/README.md",
		}
	}

	engineProjects := make([]buildadapter.ProjectInfo, len(projects))
	for i, p := range projects {
		engineProjects[i] = buildadapter.ProjectInfo{Name: p.Name, Path: p.Path}
	}
	refinement := buildadapter.Refine(context.Background(), engine, engineName, buildadapter.QueryRequest{
		RepoDir:         repoDir,
		UniversePattern: universePattern,
		ChangedPaths:    changedPaths,
		Timeout:         engineTimeout,
	}, engineProjects)
	out.BuildRefinement = &refinement
	if refinement.RunEverything {
		out.Result.RunEverything = true
	}
	return out, nil
}
