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
	// Direct marks a project whose OWN paths were touched (or that a
	// snapshot diff named as impacted), as opposed to one pulled into the
	// closure purely via depends_on edges. Feeds §14.5.9's per-check
	// run_when classes: direct-only checks (unit lanes) skip
	// closure-affected dependents. When Result.RunEverything is true,
	// consumers MUST treat every project as direct (fail closed) - the
	// flag here reflects only what path attribution proved.
	Direct bool
}

// Options configures Compute. The zero value is the conservative default.
type Options struct {
	// RootInvalidationPatterns are glob-style patterns (path.Match syntax,
	// plus a "prefix/**" form for whole-subtree matches) identifying
	// tooling/root paths that force run_everything when touched, e.g.
	// "go.mod", "Makefile", ".github/workflows/**". ORDERED,
	// first-match-wins, with a gitignore-style "!" prefix for exceptions
	// (§14.5.8) - the same dialect as ProsePatterns - so a known-safe file
	// can be carved out of a broad pattern ("!.github/workflows/ci.yml"
	// before ".github/**"; the exception must precede the pattern it
	// excepts). An excepted path does not escalate; it falls through to
	// prose/ownership attribution below. Exceptions carry the §14.5.7-style
	// obligation: name what still gates the excepted path. Flag-supplied
	// patterns are appended after the tree's and so can never override a
	// tree-declared exception's precedence (additive escalation only).
	RootInvalidationPatterns []string
	// RefinablePatterns is the subset of RootInvalidationPatterns the
	// manifests mark {refinable: true} (§14.5.8): GRAPH-VISIBLE patterns
	// whose escalation a successful build-graph snapshot diff may replace.
	// Matched by string identity against the deciding pattern, so entries
	// must be spelled exactly as they appear in RootInvalidationPatterns.
	// Only informs Result.EscalationRefinableOnly (and the RefinableHandled
	// mode below); escalation itself is unchanged.
	RefinablePatterns []string
	// RefinableHandled treats a refinable-pattern escalation as already
	// handled by a SUCCESSFUL snapshot diff: the matching path does not
	// escalate and instead falls through to prose/ownership attribution,
	// exactly like a "!" exception. Callers may set this ONLY after a
	// snapshot diff succeeded (runko-ci's orchestration) - the fail-closed
	// default is false, and blunt patterns escalate regardless.
	RefinableHandled bool
	// Strictness is StrictnessConservative (default) or StrictnessAggressive.
	// Conservative treats any changed path that matches no project and no
	// root-invalidation pattern as if it had - fail closed, never fail open
	// to "run nothing" (§14.5.3). Aggressive simply drops such paths.
	Strictness string
	// ProsePatterns is §14.5.7's de-escalation dual of root invalidation:
	// an ORDERED, first-match-wins list (same glob dialect, plus a
	// gitignore-style "!" prefix meaning "not prose"). A changed path whose
	// first match is un-negated is re-attributed to the repo-root project
	// (Path == "") instead of its longest-prefix owner - it drives the root
	// project's content-tier checks (the closure applies to that attribution
	// as usual; a root project has no dependents in practice). Root
	// invalidation always wins (checked first); without a root project the
	// path falls through to ordinary ownership. Check derivation only:
	// owner derivation reads raw touched paths and never consults this.
	ProsePatterns []string
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
	// EscalationRefinableOnly is meaningful only when RunEverything is
	// true: every escalation event was a refinable-pattern match (§14.5.8)
	// - no blunt pattern, no unowned-path conservative escalation. It is
	// the precondition for attempting a snapshot diff; anything mixed
	// stays run_everything with no refinement attempted.
	EscalationRefinableOnly bool
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

	rootProject, hasRoot := findRootProject(projects)

	refinable := make(map[string]bool, len(opts.RefinablePatterns))
	for _, p := range opts.RefinablePatterns {
		refinable[p] = true
	}

	reasons := map[string]bool{}
	runEverything := false
	escalatedBlunt := false
	escalatedRefinable := false
	direct := map[string]bool{}

	for _, cp := range paths {
		// Root invalidation BEFORE ownership: §14.5.2's semantic is
		// "this path invalidates every project", which must hold even
		// when a project (typically a root glue manifest at path "")
		// happens to own the path by longest-prefix. Owner-first made
		// tree-declared patterns dead the moment a root project existed.
		if pat, escalates := MatchOrderedWhich(opts.RootInvalidationPatterns, cp); escalates {
			if refinable[pat] && opts.RefinableHandled {
				// A successful snapshot diff already owns this path's
				// build impact; it re-enters the normal pipeline below
				// exactly like a "!" exception (§14.5.8).
			} else {
				if refinable[pat] {
					escalatedRefinable = true
				} else {
					escalatedBlunt = true
				}
				runEverything = true
				reasons[ReasonRootInvalidation] = true
				continue
			}
		}
		// Prose AFTER invalidation, BEFORE ownership (§14.5.7): content
		// paths gate as root-project content instead of running their
		// folder-owner's (and its dependents') full check set. Only with
		// a root project to attribute to - otherwise fall through, so
		// de-escalation can never widen to "run nothing" (§14.5.3).
		if hasRoot && MatchOrdered(opts.ProsePatterns, cp) {
			direct[rootProject.Name] = true
			reasons[ReasonDirectPath] = true
			continue
		}
		if owner, ok := findOwner(projects, cp); ok {
			direct[owner.Name] = true
			reasons[ReasonDirectPath] = true
			continue
		}
		if opts.Strictness != StrictnessAggressive {
			// Conservative default: an unowned path we can't scope is treated
			// like an (implicit) root/tooling change, per §14.5.3 - and never
			// a refinable one: no manifest vouched for it, so no graph diff
			// may stand in for it.
			runEverything = true
			escalatedBlunt = true
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
		refs = append(refs, ProjectRef{Name: p.Name, Path: p.Path, Direct: direct[name]})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })

	var reasonList []string
	for _, r := range []string{ReasonDirectPath, ReasonDependsOn, ReasonRootInvalidation} {
		if reasons[r] {
			reasonList = append(reasonList, r)
		}
	}

	return Result{
		ComputationID:           computationID(paths),
		Projects:                refs,
		Paths:                   paths,
		ReasonCodes:             reasonList,
		RunEverything:           runEverything,
		EscalationRefinableOnly: runEverything && escalatedRefinable && !escalatedBlunt,
	}
}

// CloseOverDependentNames returns the named projects plus every transitive
// dependent (the same reverse-edge walk Compute performs), as sorted refs.
// Exported for §14.5.8's snapshot-diff orchestration: diff-impacted targets
// map to project names, and those projects' declared dependents - including
// cross-territory ones the build graph cannot see (web -> proto) - must
// ride along exactly as they would for a changed path. Unknown names are
// dropped (a caller-side mapping bug must not invent projects).
func CloseOverDependentNames(projects []ProjectInfo, names []string) []ProjectRef {
	byName := make(map[string]ProjectInfo, len(projects))
	for _, p := range projects {
		byName[p.Name] = p
	}
	seed := map[string]bool{}
	for _, n := range names {
		if _, ok := byName[n]; ok {
			seed[n] = true
		}
	}
	closed, _ := closeOverDependents(projects, seed)
	refs := make([]ProjectRef, 0, len(closed))
	for name := range closed {
		// Seed projects were named impacted by the graph diff - the moral
		// equivalent of touched paths, so their direct-class checks run;
		// dependents added by the walk are closure-shaped (§14.5.9).
		refs = append(refs, ProjectRef{Name: name, Path: byName[name].Path, Direct: seed[name]})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs
}

// findRootProject returns the repo-root project (Path == ""), if any.
func findRootProject(projects []ProjectInfo) (ProjectInfo, bool) {
	for _, p := range projects {
		if p.Path == "" {
			return p, true
		}
	}
	return ProjectInfo{}, false
}

// MatchOrdered evaluates an ordered pattern list against one path: entries
// are tried in order, the FIRST whose pattern matches decides, and a "!"
// prefix negates. It is the one evaluator for both ordered-list manifest
// keys: prose (§14.5.7 - "!" means "this is NOT prose", how load-bearing
// files that tests consume as data are excepted from a broad "**/*.md")
// and root_invalidation (§14.5.8 - "!" means "this does NOT escalate", how
// a post-land-only workflow file is excepted from ".github/**"). No match
// means false. Exported beside MatchPath for the same reason: one
// implementation of the dialect, not one per package.
func MatchOrdered(orderedPatterns []string, changedPath string) bool {
	_, matched := MatchOrderedWhich(orderedPatterns, changedPath)
	return matched
}

// MatchOrderedWhich is MatchOrdered plus WHICH entry decided: the deciding
// pattern exactly as written in the list (a "!"-negated decider reports
// matched=false and is returned with its "!" prefix intact). §14.5.8's
// refinable check keys on this - Options.RefinablePatterns entries are
// compared by string identity against the deciding pattern.
func MatchOrderedWhich(orderedPatterns []string, changedPath string) (decidingPattern string, matched bool) {
	for _, entry := range orderedPatterns {
		pat := entry
		negated := strings.HasPrefix(pat, "!")
		if negated {
			pat = pat[1:]
		}
		if MatchPath(pat, changedPath) {
			return entry, !negated
		}
	}
	return "", false
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

// MatchPath supports path.Match glob syntax, plus a "prefix/**" form for
// matching a whole subtree (path.Match's "*" does not cross "/") and a
// leading "**/" form for any-depth matches ("**/*.md" matches README.md,
// docs/design.md, and a/b/c.md alike - the remainder is tried against the
// path itself and every "/"-suffix of it). Exported for reuse anywhere
// else in the codebase that needs the same glob dialect (e.g. receive/'s
// AgentPolicy.DenylistPaths) - keep this the one implementation rather
// than duplicating it per package.
func MatchPath(pattern, changedPath string) bool {
	// "**/" prefix before "/**" suffix, so "**/spec/**" recurses into the
	// any-depth form (whose remainder then uses the suffix form) instead of
	// treating "**/spec" as a literal prefix.
	if rest, ok := strings.CutPrefix(pattern, "**/"); ok {
		for sub := changedPath; ; {
			if MatchPath(rest, sub) {
				return true
			}
			i := strings.IndexByte(sub, '/')
			if i < 0 {
				return false
			}
			sub = sub[i+1:]
		}
	}
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
