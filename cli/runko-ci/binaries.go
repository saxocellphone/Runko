// runko-ci binaries - the standalone-release half of the deploy capability's
// executor contract (docs/spec/deploy/README.md, 2026-07-24): resolve, from
// the checkout, which rolling binary releases a base..head range must
// republish and every binary each one ships, so the binary-release workflow
// is a generic executor of tree-declared policy instead of hardcoding
// package paths, project names, release tags - or the project's dependency
// list, which the retired hand-maintained version had already let drift.
//
// The rebuild rule is the declaring project's own affected membership
// (platform/deploy.BinaryReleasesForAffected): simpler than images because
// there is no rider edge, and the dependency closure already answers "did
// anything this builds from change". Like `images`, engineName stays "" -
// a release set must be fail-closed, and RunEverything republishes every
// declared release.
package main

import (
	"github.com/saxocellphone/runko/platform/deploy"
)

// BinariesOutput is what `runko-ci binaries` prints (always JSON). Releases
// is empty for a range that touches no binary-declaring project - the
// workflow skips publishing entirely.
type BinariesOutput struct {
	RunEverything bool                   `json:"run_everything"`
	Releases      []deploy.BinaryRelease `json:"releases"`
}

// Binaries computes the binary-release matrix for a base..head range. It
// shares affectedRefined with `affected`/`checks`/`images`, so the closure
// it reads is identical to the one the merge gate sees.
func Binaries(repoDir, base, head string, rootInvalidationPatterns []string) (BinariesOutput, error) {
	out, indexed, err := affectedRefined(repoDir, base, head, rootInvalidationPatterns, "", "", 0, false)
	if err != nil {
		return BinariesOutput{}, err
	}
	releases, err := deploy.BinaryReleasesForAffected(out.Result, indexed)
	if err != nil {
		return BinariesOutput{}, err
	}
	return BinariesOutput{RunEverything: out.Result.RunEverything, Releases: releases}, nil
}
