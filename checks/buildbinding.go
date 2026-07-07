package checks

import "fmt"

// DefaultBuildCheckSetPolicy is the check-set policy project create wires
// by default when a project enables the "build" capability (docs/design.md
// §14.5.4's golden path: "default check-sets run `bazel test` over refined
// targets"). One bazel_test:<project> check per bound project, scoped like
// any other check-set (§14.4.2) - callers resolve "affected" to a concrete
// project list the same way they do for any other CheckSet.
func DefaultBuildCheckSetPolicy() CheckSetPolicy {
	return CheckSetPolicy{Pattern: "bazel_test:*", Scope: "affected"}
}

// RequireBuildBindingBlockers returns one plain-language blocker (§6.6) per
// project in unboundProjects - projects touched by a Change that lack a
// "build" capability binding. Pure: the caller is responsible for deciding
// require_build_binding is actually enabled for the org and for resolving
// which of the Change's affected projects lack the capability (docs/design.md
// §13.5's table row, §14.5.4's "org-level mandate, opt-in, not platform
// law"). Passing an empty/nil slice - the right choice when the org hasn't
// opted in - contributes no blockers, matching how staleCheckNames works in
// ComputeMergeRequirements.
func RequireBuildBindingBlockers(unboundProjects []string) []string {
	if len(unboundProjects) == 0 {
		return nil
	}
	blockers := make([]string, 0, len(unboundProjects))
	for _, name := range unboundProjects {
		blockers = append(blockers, fmt.Sprintf("%s has no build-graph binding (org requires one: require_build_binding)", name))
	}
	return blockers
}
