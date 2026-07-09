// Package buildadapter is the engine-agnostic build-graph adapter contract
// (docs/design.md §14.5.4, docs/spec/build-adapter/README.md). The
// platform's own affected computation (affected.Compute: paths + declared
// deps) is the floor - correct with zero build tooling, never required
// (NG4). A build graph, when an org has one, refines that floor to
// target-level precision. This package owns only the refinement step and
// its fail-closed wrapping; concrete engines (bazel/) shell out to the
// engine's own query tool and never run anywhere but a CI runner - the
// platform daemon never executes customer build tooling.
package buildadapter

import (
	"context"
	"path"
	"time"
)

// Engine queries one build system's dependency graph. Implementations shell
// out to the engine's own query tool - never reimplement graph traversal.
// Query MUST return an error for any ambiguity (a path outside the graph's
// view of the world, a timeout, a version mismatch) rather than guessing;
// Refine is what turns an error into fail-closed run_everything, so an
// Engine that silently under-reports would defeat the whole contract.
type Engine interface {
	Query(ctx context.Context, req QueryRequest) (QueryResult, error)
}

// QueryRequest is one refinement query.
type QueryRequest struct {
	// RepoDir is a working tree already checked out at head_sha (the
	// checkout contract, §14.4.4) - the adapter never clones separately.
	RepoDir         string
	UniversePattern string // e.g. "//..."; engines should default this if empty
	ChangedPaths    []string
	Timeout         time.Duration // zero means no engine-imposed timeout
}

// QueryResult is one engine's raw answer: affected target labels in the
// engine's native format (e.g. "//commerce/checkout:go_default_test").
type QueryResult struct {
	Targets []string
}

// ProjectInfo is the minimal project shape Refine needs to resolve target
// labels back to project names - deliberately independent of affected.
// ProjectInfo/index.IndexedProject so this package has no dependency on
// either (callers already have one of those and can convert trivially).
type ProjectInfo struct {
	Name string
	Path string
}

// Refinement is one Refine() call's outcome - JSON tags match
// docs/spec/build-adapter/refinement.schema.json field-for-field (this is
// the wire shape, unlike affected.Result which predates a schema for its
// own JSON output).
type Refinement struct {
	Engine          string            `json:"engine"`
	UniversePattern string            `json:"universe_pattern,omitempty"`
	Targets         []string          `json:"targets,omitempty"`
	TargetProjects  map[string]string `json:"target_projects,omitempty"` // target label -> project name
	RunEverything   bool              `json:"run_everything"`
	FailureReason   string            `json:"failure_reason,omitempty"` // set only when RunEverything is true because of THIS adapter
}

// Refine is the fail-closed wrapper every call site uses instead of calling
// Engine.Query directly (docs/spec/build-adapter/README.md §1's table): ANY
// error from Query - query failure, non-zero exit, timeout, unparseable
// output, missing engine binary - produces RunEverything=true. There is no
// partial-success path: an engine that half-answered is treated exactly
// like one that didn't answer at all (§14.5.3's fail-closed rule applied to
// this layer).
func Refine(ctx context.Context, engine Engine, engineName string, req QueryRequest, projects []ProjectInfo) Refinement {
	result, err := engine.Query(ctx, req)
	if err != nil {
		return Refinement{
			Engine:        engineName,
			RunEverything: true,
			FailureReason: err.Error(),
		}
	}

	universe := req.UniversePattern
	if universe == "" {
		universe = "//..."
	}
	return Refinement{
		Engine:          engineName,
		UniversePattern: universe,
		Targets:         result.Targets,
		TargetProjects:  mapTargetsToProjects(result.Targets, projects),
		RunEverything:   false,
	}
}

// mapTargetsToProjects resolves each target label's package directory back
// to a Runko project via the same longest-path-prefix rule affected.Compute
// uses for changed paths (see docs/spec/build-adapter/README.md §5) - project
// boundaries are already known from index.Scan, so this needs no second
// engine query.
func mapTargetsToProjects(targets []string, projects []ProjectInfo) map[string]string {
	if len(targets) == 0 || len(projects) == 0 {
		return nil
	}
	out := make(map[string]string, len(targets))
	for _, target := range targets {
		dir := targetPackageDir(target)
		if name, ok := longestPrefixProject(dir, projects); ok {
			out[target] = name
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// targetPackageDir extracts a label's package directory, e.g.
// "//commerce/checkout:go_default_test" -> "commerce/checkout", and
// "//:tool" -> "".
func targetPackageDir(label string) string {
	rest := label
	if len(rest) >= 2 && rest[:2] == "//" {
		rest = rest[2:]
	}
	if i := indexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// longestPrefixProject finds the project whose Path is the longest prefix
// of dir (path-component-aware, so "commerce/checkout-v2" never matches
// project path "commerce/checkout").
func longestPrefixProject(dir string, projects []ProjectInfo) (string, bool) {
	bestLen := -1
	bestName := ""
	for _, p := range projects {
		if p.Path == dir || dir == path.Clean(p.Path) {
			if len(p.Path) > bestLen {
				bestLen, bestName = len(p.Path), p.Name
			}
			continue
		}
		prefix := p.Path + "/"
		if len(p.Path) > 0 && len(dir) > len(prefix) && dir[:len(prefix)] == prefix {
			if len(p.Path) > bestLen {
				bestLen, bestName = len(p.Path), p.Name
			}
		}
	}
	if bestLen < 0 {
		return "", false
	}
	return bestName, true
}
