// runko-ci images - the §14.9.1 executor half of the deploy capability
// (§14.10): resolve, from the checkout, which deployable images a base..head
// range must rebuild AND how to build each (context, dockerfile, build args),
// so the image-build workflow is a GENERIC executor of tree-declared policy
// instead of hardcoding image names, contexts, or build args.
//
// This shares platform/deploy's derivation with the daemon's deploy record
// (runkod's openDeployRecordForLand): once both resolve the image set through
// platform/deploy.ImagesForAffected they cannot disagree by construction. Land
// ordering matters - the daemon flip must precede the workflow flip; until the
// daemon's flip lands, the two agree only by the exact hardcoded-map/derivation
// equivalence (which the retired-map replay test pins). Under-building strands
// a rollout (a deploy record completes only when every expected image reports),
// so the executor stays fail-closed: see the engineName note below.
package main

import (
	"github.com/saxocellphone/runko/platform/deploy"
)

// ImagesOutput is what `runko-ci images` prints (always JSON, like affected
// and checks). RunEverything rebuilds every declared image (fail closed,
// §14.5.3); Images is empty for a landing that touches no deployable image
// (docs-only, a library, an org that declares none). GitOps is the root
// manifest's deploy_gitops write-back target, absent when undeclared - the
// pin job reads its repository/kustomization from here and skips on
// absence, so the workflow hardcodes neither (docs/spec/deploy/README.md).
type ImagesOutput struct {
	RunEverything bool                 `json:"run_everything"`
	Images        []deploy.ImageBuild  `json:"images"`
	GitOps        *deploy.GitOpsTarget `json:"gitops,omitempty"`
}

// Images computes the image-build matrix for a base..head range: the affected
// closure mapped through the tree's deploy.image / deploy.workloads
// declarations (platform/deploy.ImageBuildsForAffected). It shares
// affectedRefined with `affected`/`checks`, so the closure it reads is
// identical to the one the merge gate and check executor see.
//
// engineName is deliberately "" (no build-graph refinement): a deploy build
// set must be FAIL-CLOSED. The daemon's deploy record uses no engine
// (computeAffectedForChange), so any snapshot-diff narrowing here could shrink
// the built set below the record's expected set and strand the rollout. Do NOT
// add an --engine flag to this command without resolving that.
func Images(repoDir, base, head string, rootInvalidationPatterns []string) (ImagesOutput, error) {
	out, indexed, err := affectedRefined(repoDir, base, head, rootInvalidationPatterns, "", "", 0, false)
	if err != nil {
		return ImagesOutput{}, err
	}
	builds, err := deploy.ImageBuildsForAffected(out.Result, indexed)
	if err != nil {
		return ImagesOutput{}, err
	}
	return ImagesOutput{RunEverything: out.Result.RunEverything, Images: builds, GitOps: deploy.RootGitOps(indexed)}, nil
}
