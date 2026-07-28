// Auto-approve zones (2026-07-28): the tree-declared bootstrap posture.
//
// A project whose manifest says `auto_approve: true` declares its subtree a
// zone where the merge gate stops waiting on humans - owner requirements read
// as satisfied, agent-policy findings as acknowledged - so an agent scaffolding
// a brand-new project lands its own work unattended. It waives APPROVALS only:
// required checks still gate, because "nobody read it" is a governance choice
// and "nobody built it" is just breakage.
//
// The declaration INHERITS down the project tree rather than applying to one
// project's own paths: the nearest ancestor project that declares the field
// decides. That is what makes the root manifest the whole-repo switch (declare
// it once, every path is auto) while a nested project stays free to carve
// itself back out with an explicit `auto_approve: false` - the same
// nearest-declaration rule OWNERS already uses for ownership, applied to
// governance.
//
// Who resolves this against WHICH tree is a security property, not a detail:
// callers must scan TRUNK, never a change's own head, or a change could enable
// auto-approve in its own manifest and thereby approve itself. runkod's merge
// gate does exactly that (runkod/api.go), and an org-level kill switch
// (disable_auto_approve) vetoes the whole mechanism deployment-wide.
package index

import "strings"

// AutoApproved reports whether a repo-relative path sits in an auto-approve
// zone: the longest-prefix project that DECLARES auto_approve decides, and an
// undeclared project inherits from its nearest declaring ancestor. No
// declaration anywhere means false - the ordinary governed posture, which is
// what every repo that has never heard of this field gets.
func AutoApproved(projects []IndexedProject, path string) bool {
	decided := false
	value := false
	best := -1
	for _, p := range projects {
		if p.AutoApprove == nil {
			continue // undeclared: transparent, its ancestor still decides
		}
		if !ownsPath(p.Path, path) {
			continue
		}
		if len(p.Path) > best {
			best = len(p.Path)
			value = *p.AutoApprove
			decided = true
		}
	}
	if !decided {
		return false
	}
	return value
}

// AutoApprovedAll reports whether EVERY path is auto-approved - the question
// the change-wide gates ask (a change's agent-policy findings are one verdict
// over the whole diff, so a single governed path keeps the human in the loop).
// An empty path set is not a zone: false, fail closed.
func AutoApprovedAll(projects []IndexedProject, paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		if !AutoApproved(projects, path) {
			return false
		}
	}
	return true
}

// ownsPath is the longest-prefix containment rule affected.Compute and the
// owner gate already share: a repo-root project (Path "") contains everything,
// otherwise the path is the project dir itself or lives under it.
func ownsPath(projectPath, path string) bool {
	if projectPath == "" || projectPath == "." {
		return true
	}
	return path == projectPath || strings.HasPrefix(path, projectPath+"/")
}
