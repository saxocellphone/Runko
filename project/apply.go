package project

import (
	"fmt"
	"path"

	"github.com/saxocellphone/runko/core"
)

// Apply commits a Plan's files as a single overlay, rooted at plan.Path
// (§10.1's "Apply" step) - the single write path; there is no "edit four
// files consistently" step (§10.3). base may be "" to create a root commit
// in an empty repository.
func Apply(store core.MonorepoStore, base core.Revision, plan Plan, meta core.CommitMeta) (core.Revision, error) {
	if meta.Message == "" {
		meta.Message = fmt.Sprintf("Create project %s", plan.EffectiveManifest.Name)
	}

	overlay := core.Overlay{Changes: make([]core.FileChange, 0, len(plan.Files))}
	for _, f := range plan.Files {
		overlay.Changes = append(overlay.Changes, core.FileChange{
			Path:    path.Join(plan.Path, f.Path),
			Content: []byte(f.Content),
		})
	}

	return store.CommitOverlay(base, overlay, meta)
}
