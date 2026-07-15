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
// (docs/spec/build-adapter/README.md §2). For the rdeps strategy,
// BuildRefinement is additive information, never a replacement for Result.
// For the snapshot-diff strategy (§14.5.8), a SUCCESSFUL diff replaces a
// refinable-only run_everything escalation in Result itself - that
// replacement is the strategy's whole point, and it fails closed back to
// run_everything on any error.
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
	out, _, err := affectedRefined(repoDir, base, head, rootInvalidationPatterns, engineName, universePattern, engineTimeout, true)
	return out, err
}

// affectedRefined is the shared core of `affected` and `checks`: the
// platform floor, then - when an engine is named - one of two refinement
// strategies:
//
//   - run_everything from REFINABLE root-invalidation patterns only
//     (§14.5.8) and the engine can SnapshotDiff: diff the graph between
//     base and head; on success REPLACE the escalation with the
//     de-escalated floor union the diff-impacted projects' dependents
//     closure. Any failure keeps run_everything (fail closed).
//   - otherwise, when rdepsToo: the additive rdeps refinement (§14.5.4),
//     which never replaces the floor; its own failure escalates the whole
//     output. `checks` passes rdepsToo=false - its matrix never keys on
//     rdeps output, so the query would be pure cost, and a spurious rdeps
//     failure would escalate a perfectly scoped matrix.
func affectedRefined(repoDir, base, head string, rootInvalidationPatterns []string, engineName, universePattern string, engineTimeout time.Duration, rdepsToo bool) (AffectedOutput, []index.IndexedProject, error) {
	fc, err := computeFloor(repoDir, base, head, rootInvalidationPatterns)
	if err != nil {
		return AffectedOutput{}, nil, err
	}
	out := fc.out

	if engineName == "" {
		return out, fc.indexed, nil
	}
	engine, ok := knownEngines[engineName]
	if !ok {
		return AffectedOutput{}, nil, &clierr.Error{
			Code:       "unknown_engine",
			Field:      "--engine",
			Message:    fmt.Sprintf("unknown build-graph engine %q", engineName),
			Suggestion: "supported engines: bazel",
			DocURL:     "docs/spec/build-adapter/README.md",
		}
	}

	engineProjects := make([]buildadapter.ProjectInfo, len(fc.indexed))
	for i, p := range fc.indexed {
		engineProjects[i] = buildadapter.ProjectInfo{Name: p.Name, Path: p.Path}
	}

	if out.Result.RunEverything && out.Result.EscalationRefinableOnly {
		if differ, ok := engine.(buildadapter.SnapshotDiffer); ok {
			refinement := buildadapter.RefineSnapshot(context.Background(), differ, engineName, buildadapter.SnapshotDiffRequest{
				RepoDir:         repoDir,
				BaseRev:         base,
				HeadRev:         head,
				UniversePattern: universePattern,
				Timeout:         engineTimeout,
			}, engineProjects)
			out.BuildRefinement = &refinement
			if !refinement.RunEverything {
				if result, ok := deescalate(fc, refinement); ok {
					out.Result = result
				}
			}
			// Snapshot-diff was the applicable strategy; rdeps over the
			// changed paths would answer a different (weaker) question.
			return out, fc.indexed, nil
		}
		// Engine cannot snapshot-diff: run_everything stands; fall through
		// so the output still carries the advisory rdeps refinement.
	}

	if !rdepsToo {
		return out, fc.indexed, nil
	}

	changedPaths := out.Result.Paths
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
	return out, fc.indexed, nil
}

// deescalate recomputes the floor with the refinable escalations handled
// (their paths re-enter prose/ownership attribution), then unions in the
// snapshot-diff-impacted projects and closes over declared dependents -
// cross-territory edges (web -> proto) included, which the build graph
// cannot see. Returns ok=false (keep run_everything) if the de-escalated
// floor itself still escalates - e.g. a refinable path that no project
// owns falls back to §14.5.3's conservative rule.
func deescalate(fc floorComputation, refinement buildadapter.Refinement) (affected.Result, bool) {
	opts := fc.opts
	opts.RefinableHandled = true
	result := affected.Compute(fc.projects, fc.changedPaths, opts)
	if result.RunEverything {
		return affected.Result{}, false
	}

	// Seed the closure with DIRECT floor members + diff-impacted projects
	// only: CloseOverDependentNames marks its seed Direct (§14.5.9), and
	// re-derives dependents itself. Seeding the floor's closure-pulled
	// members too would stamp them Direct and wrongly run their
	// direct-only lanes (race checks) on a merely-dependent project.
	names := make([]string, 0, len(result.Projects)+len(refinement.TargetProjects))
	for _, p := range result.Projects {
		if p.Direct {
			names = append(names, p.Name)
		}
	}
	for _, name := range refinement.TargetProjects {
		names = append(names, name)
	}
	result.Projects = affected.CloseOverDependentNames(fc.projects, names)
	return result, true
}

// floorComputation carries everything the refinement strategies need to
// recompute or extend the platform floor without re-scanning the tree.
type floorComputation struct {
	out          AffectedOutput
	indexed      []index.IndexedProject
	projects     []affected.ProjectInfo
	changedPaths []string
	opts         affected.Options
}

// computeFloor is the platform-floor core: scan the head tree, diff,
// compute the closure with tree-declared root-invalidation patterns (§9.4)
// plus any flag-supplied ones - and hand back the scan and inputs so
// callers can resolve manifest-declared policy (check definitions) and
// re-run Compute under §14.5.8's RefinableHandled mode.
func computeFloor(repoDir, base, head string, rootInvalidationPatterns []string) (floorComputation, error) {
	store := gitstore.New(repoDir)

	indexed, err := index.Scan(store, core.Revision(head), nil)
	if err != nil {
		return floorComputation{}, clierr.WrapRevisionError(fmt.Errorf("scan projects at %s: %w", head, err), "--head", head)
	}
	projects := index.AffectedProjectInfos(indexed)

	changedPaths, err := diffPaths(repoDir, base, head)
	if err != nil {
		// Match against the raw git error BEFORE adding the "diff %s..%s"
		// context below - that prefix mentions both base and head verbatim,
		// which would make WrapRevisionErrorAmong's substring match pick
		// whichever candidate a map iteration happened to visit first,
		// rather than the one git's own message actually names.
		if wrapped := clierr.WrapRevisionErrorAmong(err, map[string]string{"--base": base, "--head": head}); wrapped != err {
			return floorComputation{}, wrapped
		}
		return floorComputation{}, fmt.Errorf("diff %s..%s: %w", base, head, err)
	}

	opts := affected.Options{
		// Tree-declared patterns (root manifest, §9.4) plus the flag's.
		RootInvalidationPatterns: append(index.RootInvalidation(indexed), rootInvalidationPatterns...),
		RefinablePatterns:        index.RootInvalidationRefinable(indexed),
		ProsePatterns:            index.Prose(indexed),
	}
	result := affected.Compute(projects, changedPaths, opts)
	return floorComputation{
		out:          AffectedOutput{Result: result},
		indexed:      indexed,
		projects:     projects,
		changedPaths: changedPaths,
		opts:         opts,
	}, nil
}
