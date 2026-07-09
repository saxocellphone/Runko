package affected

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"sort"
	"strings"
)

// Reason codes, matching docs/spec/mcp-tools/common.schema.json#/$defs/AffectedComputation.
const (
	ReasonDirectPath       = "direct_path"
	ReasonDependsOn        = "depends_on"
	ReasonRootInvalidation = "root_invalidation"
)

// Strictness values (§14.5.3). Conservative is the zero value/default:
// anything Compute can't confidently scope fails closed to run_everything.
const (
	StrictnessConservative = "conservative"
	StrictnessAggressive   = "aggressive"
)

// ProjectInfo is the minimal per-project shape Compute needs: its own
// path/name and its DECLARED dependencies (other project names it depends
// on). Inferred dependencies must never be passed here - they are
// advisory-only and never gate merges (§13.3).
type ProjectInfo struct {
	Name                 string
	Path                 string
	DeclaredDependencies []string
}

// ProjectRef is one affected project in a Result.
type ProjectRef struct {
	Name string
	Path string
}

// Options configures Compute. The zero value is the conservative default.
type Options struct {
	// RootInvalidationPatterns are glob-style patterns (path.Match syntax,
	// plus a "prefix/**" form for whole-subtree matches) identifying
	// tooling/root paths that force run_everything when touched, e.g.
	// "go.mod", "Makefile", ".github/workflows/**".
	RootInvalidationPatterns []string
	// Strictness is StrictnessConservative (default) or StrictnessAggressive.
	// Conservative treats any changed path that matches no project and no
	// root-invalidation pattern as if it had - fail closed, never fail open
	// to "run nothing" (§14.5.3). Aggressive simply drops such paths.
	Strictness string
}

// Result mirrors the "affected" block of docs/spec/webhooks/webhook-envelope.schema.json.
type Result struct {
	ComputationID string
	Projects      []ProjectRef
	Paths         []string
	ReasonCodes   []string
	// RunEverything MUST be honored by every caller (CI templates, webhooks,
	// MCP): fail closed to a broader run, never fail open (§13.3, §14.5.3).
	// Projects/Paths remain informational even when this is true - they may
	// be an incomplete view of the world, which is precisely why it's true.
	RunEverything bool
}

// Compute implements the v1 affected algorithm (§13.3): paths -> projects by
// longest-prefix match, transitive closure over DECLARED dependency edges
// (reverse direction - "who depends on what changed"), and root-invalidation
// patterns. It is a pure function: identical inputs always produce an
// identical Result, including ComputationID.
func Compute(projects []ProjectInfo, changedPaths []string, opts Options) Result {
	paths := dedupSorted(changedPaths)

	byName := make(map[string]ProjectInfo, len(projects))
	for _, p := range projects {
		byName[p.Name] = p
	}

	reasons := map[string]bool{}
	runEverything := false
	direct := map[string]bool{}

	for _, cp := range paths {
		if owner, ok := findOwner(projects, cp); ok {
			direct[owner.Name] = true
			reasons[ReasonDirectPath] = true
			continue
		}
		if matchesAny(opts.RootInvalidationPatterns, cp) {
			runEverything = true
			reasons[ReasonRootInvalidation] = true
			continue
		}
		if opts.Strictness != StrictnessAggressive {
			// Conservative default: an unowned path we can't scope is treated
			// like an (implicit) root/tooling change, per §14.5.3.
			runEverything = true
			reasons[ReasonRootInvalidation] = true
		}
	}

	affectedSet, sawDependent := closeOverDependents(projects, direct)
	if sawDependent {
		reasons[ReasonDependsOn] = true
	}

	refs := make([]ProjectRef, 0, len(affectedSet))
	for name := range affectedSet {
		p := byName[name]
		refs = append(refs, ProjectRef{Name: p.Name, Path: p.Path})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })

	var reasonList []string
	for _, r := range []string{ReasonDirectPath, ReasonDependsOn, ReasonRootInvalidation} {
		if reasons[r] {
			reasonList = append(reasonList, r)
		}
	}

	return Result{
		ComputationID: computationID(paths),
		Projects:      refs,
		Paths:         paths,
		ReasonCodes:   reasonList,
		RunEverything: runEverything,
	}
}

// findOwner returns the project owning changedPath by longest-path-prefix
// match. A project with Path == "" is a repo-root project and matches
// everything, at the lowest possible priority.
func findOwner(projects []ProjectInfo, changedPath string) (ProjectInfo, bool) {
	var best ProjectInfo
	found := false
	for _, p := range projects {
		matches := p.Path == "" || changedPath == p.Path || strings.HasPrefix(changedPath, p.Path+"/")
		if !matches {
			continue
		}
		if !found || len(p.Path) > len(best.Path) {
			best, found = p, true
		}
	}
	return best, found
}

// closeOverDependents returns the transitive closure of direct plus every
// project that (transitively) declares a dependency on a project in direct,
// following reverse edges. Safe against dependency cycles.
func closeOverDependents(projects []ProjectInfo, direct map[string]bool) (map[string]bool, bool) {
	dependents := make(map[string][]string)
	for _, p := range projects {
		for _, dep := range p.DeclaredDependencies {
			dependents[dep] = append(dependents[dep], p.Name)
		}
	}

	affected := make(map[string]bool, len(direct))
	queue := make([]string, 0, len(direct))
	for name := range direct {
		affected[name] = true
		queue = append(queue, name)
	}

	sawDependent := false
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range dependents[cur] {
			if !affected[dep] {
				affected[dep] = true
				sawDependent = true
				queue = append(queue, dep)
			}
		}
	}
	return affected, sawDependent
}

func matchesAny(patterns []string, changedPath string) bool {
	for _, pat := range patterns {
		if MatchPath(pat, changedPath) {
			return true
		}
	}
	return false
}

// MatchPath supports path.Match glob syntax, plus a "prefix/**" form for
// matching a whole subtree (path.Match's "*" does not cross "/"). Exported
// for reuse anywhere else in the codebase that needs the same glob dialect
// (e.g. receive/'s AgentPolicy.DenylistPaths) - keep this the one
// implementation rather than duplicating it per package.
func MatchPath(pattern, changedPath string) bool {
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return changedPath == prefix || strings.HasPrefix(changedPath, prefix+"/")
	}
	ok, err := path.Match(pattern, changedPath)
	return err == nil && ok
}

func dedupSorted(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	cp := append([]string(nil), paths...)
	sort.Strings(cp)
	out := cp[:1]
	for _, p := range cp[1:] {
		if p != out[len(out)-1] {
			out = append(out, p)
		}
	}
	return out
}

// computationID is deterministic in its inputs, keeping Compute a pure
// function - no clock, no randomness.
func computationID(sortedPaths []string) string {
	h := sha256.Sum256([]byte(strings.Join(sortedPaths, "\n")))
	return "aff_" + hex.EncodeToString(h[:])[:12]
}
