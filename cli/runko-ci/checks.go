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
func Checks(repoDir, base, head string, rootInvalidationPatterns []string, engineName, universePattern string, engineTimeout time.Duration) (ChecksOutput, error) {
	out, indexed, err := affectedRefined(repoDir, base, head, rootInvalidationPatterns, engineName, universePattern, engineTimeout, false)
	if err != nil {
		return ChecksOutput{}, err
	}

	byName := map[string]index.IndexedProject{}
	for _, p := range indexed {
		byName[p.Name] = p
	}

	var scoped []index.IndexedProject
	if out.RunEverything {
		scoped = indexed
	} else {
		for _, ref := range out.Projects {
			if p, ok := byName[ref.Name]; ok {
				scoped = append(scoped, p)
			}
		}
	}

	seen := map[string]CheckRun{}
	var runs []CheckRun
	for _, p := range scoped {
		for _, c := range p.Checks {
			if prev, ok := seen[c.Name]; ok {
				if prev.Command != c.Command {
					return ChecksOutput{}, &clierr.Error{
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
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Name < runs[j].Name })

	return ChecksOutput{RunEverything: out.RunEverything, Checks: runs, BuildRefinement: out.BuildRefinement}, nil
}
