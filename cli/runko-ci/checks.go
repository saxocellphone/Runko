// runko-ci checks - the encapsulation half of §14.9's CI contract: resolve
// (affected projects -> their manifest-declared checks) from the checkout,
// so a CI system can be a GENERIC executor of tree-declared policy instead
// of hardcoding project names, commands, or environments. The merge gate
// resolves required check NAMES from the same head tree (runkod
// computeAffected scans at change.HeadSHA), so what this command returns
// and what the gate demands can never disagree - which is what makes
// adding or renaming a check a single-manifest change with no CI-config
// coupling.
package main

import (
	"fmt"
	"sort"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/buildadapter"
	"github.com/saxocellphone/runko/platform/index"
)

// CheckRun is one resolved check the executor should run: the project that
// declared it, the name to report under, and the command to execute.
type CheckRun struct {
	Project string `json:"project"`
	Name    string `json:"name"`
	Command string `json:"command"`
}

// ChecksOutput is what `runko-ci checks` prints. BuildRefinement is
// present only when --engine was passed AND a refinement ran - under the
// snapshot-diff strategy (§14.5.8) it is the audit trail for why
// run_everything did (or did not) narrow to a scoped matrix.
type ChecksOutput struct {
	RunEverything   bool                     `json:"run_everything"`
	Checks          []CheckRun               `json:"checks"`
	BuildRefinement *buildadapter.Refinement `json:"build_refinement,omitempty"`
}

// Checks computes the check matrix for a base..head range: the affected
// project closure (or every project under run_everything - fail closed,
// §14.5.3), mapped to each project's manifest-declared ci.checks. Checks
// deduplicate by name (several projects may declare the same shared check);
// the same name with DIFFERENT commands is a structured error - silent
// first-wins would execute one project's command while the gate credits
// another's. A non-empty engineName enables §14.5.8's snapshot-diff
// narrowing of refinable-only escalations; the gate does NOT consume that
// narrowing, so callers must only pass an engine where nothing gates on
// the result (post-land CI) until gate-grade refinement lands (§14.5.4).
//
// postLand additionally selects EVERY project's post_land-class checks
// (index.PostLandChecks), unscoped by the affected result - that class runs
// on every land by definition (its subject is the deployment artifact, not
// one project's code). Only post-land CI passes it: the merge gate resolves
// through ChecksFor, which never returns the class, so a pre-land caller
// passing it would run checks the gate cannot credit.
func Checks(repoDir, base, head string, rootInvalidationPatterns []string, engineName, universePattern string, engineTimeout time.Duration, postLand bool) (ChecksOutput, error) {
	out, indexed, err := affectedRefined(repoDir, base, head, rootInvalidationPatterns, engineName, universePattern, engineTimeout, false)
	if err != nil {
		return ChecksOutput{}, err
	}

	byName := map[string]index.IndexedProject{}
	for _, p := range indexed {
		byName[p.Name] = p
	}

	type scopedProject struct {
		p      index.IndexedProject
		direct bool
	}
	var scoped []scopedProject
	if out.RunEverything {
		// Fail closed (§14.5.9): run_everything treats every project as
		// direct - both check classes execute.
		for _, p := range indexed {
			scoped = append(scoped, scopedProject{p: p, direct: true})
		}
	} else {
		for _, ref := range out.Projects {
			if p, ok := byName[ref.Name]; ok {
				scoped = append(scoped, scopedProject{p: p, direct: ref.Direct})
			}
		}
	}

	seen := map[string]CheckRun{}
	var runs []CheckRun
	add := func(p index.IndexedProject, defs []index.CheckDef) error {
		for _, c := range defs {
			if prev, ok := seen[c.Name]; ok {
				if prev.Command != c.Command {
					return &clierr.Error{
						Code: "ambiguous_check", Field: "ci.checks",
						Message: fmt.Sprintf("check %q is declared with different commands by projects %q (%s) and %q (%s)",
							c.Name, prev.Project, prev.Command, p.Name, c.Command),
						Suggestion: "give the checks distinct names, or align the commands - the executor must know which one to run",
					}
				}
				continue
			}
			run := CheckRun{Project: p.Name, Name: c.Name, Command: c.Command}
			seen[c.Name] = run
			runs = append(runs, run)
		}
		return nil
	}
	for _, sp := range scoped {
		// index.ChecksFor is the shared §14.5.9 rule the merge gate also
		// resolves through (runkod requiredCheckNames) - one function, so
		// gate and executor can never disagree on a change's check set.
		if err := add(sp.p, index.ChecksFor(sp.p, sp.direct)); err != nil {
			return ChecksOutput{}, err
		}
	}
	if postLand {
		// Every project's post_land checks, not just the affected closure's:
		// the class runs on every land. Same dedupe map, so a post_land
		// check colliding with a gate-class name under a different command
		// is the same ambiguous_check refusal.
		for _, p := range indexed {
			if err := add(p, index.PostLandChecks(p)); err != nil {
				return ChecksOutput{}, err
			}
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Name < runs[j].Name })

	return ChecksOutput{RunEverything: out.RunEverything, Checks: runs, BuildRefinement: out.BuildRefinement}, nil
}
