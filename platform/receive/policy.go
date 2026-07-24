package receive

import (
	"fmt"
	"strings"

	"github.com/saxocellphone/runko/platform/affected"
)

// AgentPolicy mirrors docs/design.md §8.7 (db/migrations' agent_policies
// table is the persisted form of the same shape). Keep in sync by hand until
// codegen exists (same debt as project.Manifest, see project/doc.go).
type AgentPolicy struct {
	RequireWorkspaceAffinity bool `json:"require_workspace_affinity"`
	// RequireDescription makes the §8.6 change description (`runko change
	// describe`) mandatory for an agent's changes: its absence becomes a
	// MERGE blocker (runkod's mergeRequirements, §8.7 gate on §8.6 state) -
	// so an agent cannot land work no reviewer can read without the diff.
	// Unlike the size caps this is NOT a receive-time gate (the blurb is set
	// after the push, and never derived from the commit message, §8.6); it
	// is evaluated at land/merge-requirements time against change.Description.
	RequireDescription    bool     `json:"require_description"`
	MaxChangedFiles       int      `json:"max_changed_files"`
	MaxDiffBytes          int64    `json:"max_diff_bytes"`
	CanCreateProjects     bool     `json:"can_create_projects"`
	CanLandChanges        bool     `json:"can_land_changes"`
	CanModifyOwners       bool     `json:"can_modify_owners"`
	CanEnableCapabilities []string `json:"can_enable_capabilities"`
	DenylistPaths         []string `json:"denylist_paths"` // glob patterns, affected.MatchPath syntax
}

// DefaultAgentPolicy mirrors the illustrative org defaults in §8.7.
func DefaultAgentPolicy() AgentPolicy {
	return AgentPolicy{
		RequireWorkspaceAffinity: true,
		RequireDescription:       true,
		MaxChangedFiles:          40,
		MaxDiffBytes:             512000,
		CanCreateProjects:        true,
		CanLandChanges:           false,
		CanModifyOwners:          false,
		CanEnableCapabilities:    []string{"http", "rpc"},
		DenylistPaths:            []string{"security/**", "**/.github/workflows/**"},
	}
}

// PushSummary is what EvaluatePolicy needs to know about an incoming push,
// gathered before secret scanning or Change creation.
type PushSummary struct {
	ChangedFiles      []string
	DiffBytes         int64
	WorkspaceAffinity []string // project-relative path roots this workspace may write; empty = no affinity configured
	// AffinityProjects are the PROJECT NAMES behind WorkspaceAffinity (the
	// workspace's project_affinity, parallel to the path roots). Optional -
	// when set, an affinity rejection names the set the way a human granted
	// it, since affinity is declared by project name, not path (FIX #5).
	AffinityProjects []string
	// ModifiesOwners means ownership of EXISTING code changes: an OWNERS
	// or PROJECT.yaml that existed at the push's base, a brand-new OWNERS
	// file anywhere (§7.3's nearest-file rule re-resolves existing paths),
	// or a new manifest atop existing content (a nested-project carve-out
	// reassigns its subtree by longest prefix). A new manifest on virgin
	// paths is NOT this - that is IsProjectCreate, gated separately: the
	// old any-manifest-path reading made CanCreateProjects dead on
	// arrival, since every scaffold carries a manifest (2026-07-16
	// dogfood review, finding 2).
	ModifiesOwners      bool
	EnabledCapabilities []string
	IsLandRequest       bool
	IsProjectCreate     bool
	// Author is the pushing principal's name; NewProjectOwners are the
	// owners: refs declared by manifests this push CREATES. Together they
	// feed the self-grant gate: an agent may scaffold a project, never as
	// its own owner (§8.7's no-self-approval, applied at birth).
	Author           string
	NewProjectOwners []string
}

// PolicyViolation mirrors the shape of
// docs/spec/mcp-tools/common.schema.json#/$defs/Error, scoped to policy
// rejections (code/message/suggestion; no retryable - a policy violation is
// never retryable unresolved).
type PolicyViolation struct {
	Code       string
	Message    string
	Suggestion string
	// Ackable classifies the violation for the 2026-07-24 enforcement
	// split (platform/README.md Decisions): an ackable violation no longer
	// refuses the push - the change is accepted and the finding becomes the
	// reserved `agent-policy` check on it, red until a human with approve
	// rights acknowledges it. Hard (non-ackable) violations still refuse at
	// receive: they are structural (provenance/affinity - merge-time cannot
	// retrofit attribution onto objects that arrived without it) or
	// action-shaped (a land request), not content a reviewer can weigh.
	Ackable bool
}

// SplitAckable partitions violations into the receive-refusing hard set and
// the accept-then-gate ackable set.
func SplitAckable(violations []PolicyViolation) (hard, ackable []PolicyViolation) {
	for _, v := range violations {
		if v.Ackable {
			ackable = append(ackable, v)
		} else {
			hard = append(hard, v)
		}
	}
	return hard, ackable
}

// EvaluatePolicy enforces AgentPolicy against a push - server-side, since
// that's the only enforcement that counts (§8.4, §15.3: "never trust
// client-claimed affinity alone"). It only applies to agent principals;
// human pushes are governed by owners/branch protection, not AgentPolicy.
//
// Since 2026-07-24 each violation carries its enforcement class (Ackable):
// content-shaped findings (denylist, size caps, owners/project/capability
// gates, self-grant) mark Ackable and no longer refuse at receive - the
// funnel accepts the push and the finding becomes the reserved
// `agent-policy` check a human must acknowledge before merge. Structural
// findings (affinity/provenance) and action requests (land) stay hard.
func EvaluatePolicy(policy AgentPolicy, summary PushSummary) []PolicyViolation {
	var v []PolicyViolation

	if policy.RequireWorkspaceAffinity && len(summary.WorkspaceAffinity) == 0 {
		v = append(v, PolicyViolation{
			Code:       "workspace_affinity_required",
			Message:    "this agent policy requires a workspace with project affinity for writes",
			Suggestion: "call create_workspace with project_ids before writing",
		})
	}

	if len(summary.WorkspaceAffinity) > 0 {
		set := affinitySetLabel(summary)
		for _, f := range summary.ChangedFiles {
			if !withinAffinity(f, summary.WorkspaceAffinity) {
				v = append(v, PolicyViolation{
					Code: "path_outside_affinity",
					// Name the path AS a path and say where it maps, so a
					// repo-root file (e.g. `runko-ci`) is not misread as the
					// same-named directory (e.g. `cli/runko-ci/`) - the
					// ambiguity the old bare "%s is outside..." caused (FIX #5).
					Message:    fmt.Sprintf("%s is outside this workspace's affinity %s", describePathLocation(f), set),
					Suggestion: "add the owning project to this workspace's affinity (`runko workspace create --project <name>`) or open a new workspace for it; affinity is by PROJECT NAME (`runko project list`), not directory - a repo-root file belongs to the root project",
				})
			}
		}
	}

	if policy.MaxChangedFiles > 0 && len(summary.ChangedFiles) > policy.MaxChangedFiles {
		v = append(v, PolicyViolation{
			Code:       "max_changed_files_exceeded",
			Message:    fmt.Sprintf("changed %d files, agent policy allows at most %d", len(summary.ChangedFiles), policy.MaxChangedFiles),
			Suggestion: "split the work into a stack of smaller changes - one reviewable step each; a single push carries the whole stack",
			Ackable:    true,
		})
	}

	if policy.MaxDiffBytes > 0 && summary.DiffBytes > policy.MaxDiffBytes {
		v = append(v, PolicyViolation{
			Code:       "max_diff_bytes_exceeded",
			Message:    fmt.Sprintf("diff is %d bytes, agent policy allows at most %d", summary.DiffBytes, policy.MaxDiffBytes),
			Suggestion: "split the work into a stack of smaller changes - one reviewable step each; a single push carries the whole stack",
			Ackable:    true,
		})
	}

	for _, f := range summary.ChangedFiles {
		if pattern, matched := matchAny(policy.DenylistPaths, f); matched {
			v = append(v, PolicyViolation{
				Code:    "denylist_path",
				Message: fmt.Sprintf("%s matches denylisted pattern %q for agent writes", f, pattern),
				Ackable: true,
			})
		}
	}

	if summary.ModifiesOwners && !policy.CanModifyOwners {
		v = append(v, PolicyViolation{
			Code:    "owners_modification_denied",
			Message: "this agent policy does not allow modifying owners",
			Ackable: true,
		})
	}

	if summary.IsProjectCreate && !policy.CanCreateProjects {
		v = append(v, PolicyViolation{
			Code:    "project_create_denied",
			Message: "this agent policy does not allow creating projects",
			Ackable: true,
		})
	}

	if summary.IsProjectCreate && summary.Author != "" {
		for _, o := range summary.NewProjectOwners {
			if o == summary.Author {
				v = append(v, PolicyViolation{
					Code:       "owner_self_grant",
					Message:    fmt.Sprintf("the new project's owners name the pushing agent (%s) - an agent never grants itself ownership", o),
					Suggestion: "name your minting human in owners:, or leave owners empty to inherit",
					Ackable:    true,
				})
				break
			}
		}
	}

	if summary.IsLandRequest && !policy.CanLandChanges {
		v = append(v, PolicyViolation{
			Code:       "land_denied",
			Message:    "this agent policy requires human land approval",
			Suggestion: "request human review instead of calling land_change",
		})
	}

	allowed := make(map[string]bool, len(policy.CanEnableCapabilities))
	for _, c := range policy.CanEnableCapabilities {
		allowed[c] = true
	}
	for _, c := range summary.EnabledCapabilities {
		if !allowed[c] {
			v = append(v, PolicyViolation{
				Code:    "capability_denied",
				Message: fmt.Sprintf("this agent policy does not allow enabling capability %q", c),
				Ackable: true,
			})
		}
	}

	return v
}

// describePathLocation names a changed path AS a path and says where in the
// tree it lives, so the affinity message can't be misread as a same-named
// directory (FIX #5): a repo-root file `runko-ci` is called out as exactly
// that, not confused with `cli/runko-ci/`.
func describePathLocation(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return fmt.Sprintf("path %q (under directory %q)", p, p[:i+1])
	}
	return fmt.Sprintf("repo-root file %q", p)
}

// affinitySetLabel renders the workspace's affinity as a brace set for the
// rejection. Prefers the PROJECT NAMES (how a human granted it); falls back
// to the path roots, rendering the root project's "" as <repo root>.
func affinitySetLabel(summary PushSummary) string {
	items := summary.AffinityProjects
	if len(items) == 0 {
		items = make([]string, 0, len(summary.WorkspaceAffinity))
		for _, r := range summary.WorkspaceAffinity {
			if r == "" {
				items = append(items, "<repo root>")
			} else {
				items = append(items, r)
			}
		}
	}
	return "{" + strings.Join(items, ", ") + "}"
}

func withinAffinity(changedPath string, affinity []string) bool {
	for _, root := range affinity {
		// The ROOT project's path is "" (repo root, owning what no deeper
		// manifest claims): as an allowlist entry it grants the whole
		// tree. Without this case the prefix arithmetic below can never
		// match it (no path starts with "/"), which write-blocked every
		// agent granted root affinity (migration-findings #40).
		if root == "" {
			return true
		}
		if changedPath == root || (len(changedPath) > len(root) && changedPath[:len(root)] == root && changedPath[len(root)] == '/') {
			return true
		}
	}
	return false
}

func matchAny(patterns []string, p string) (string, bool) {
	for _, pat := range patterns {
		if affected.MatchPath(pat, p) {
			return pat, true
		}
	}
	return "", false
}
