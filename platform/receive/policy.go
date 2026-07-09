package receive

import (
	"fmt"

	"github.com/saxocellphone/runko/platform/affected"
)

// AgentPolicy mirrors docs/design.md §8.7 (db/migrations' agent_policies
// table is the persisted form of the same shape). Keep in sync by hand until
// codegen exists (same debt as project.Manifest, see project/doc.go).
type AgentPolicy struct {
	RequireWorkspaceAffinity bool
	MaxChangedFiles          int
	MaxDiffBytes             int64
	CanCreateProjects        bool
	CanLandChanges           bool
	CanModifyOwners          bool
	CanEnableCapabilities    []string
	DenylistPaths            []string // glob patterns, affected.MatchPath syntax
}

// DefaultAgentPolicy mirrors the illustrative org defaults in §8.7.
func DefaultAgentPolicy() AgentPolicy {
	return AgentPolicy{
		RequireWorkspaceAffinity: true,
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
	ChangedFiles        []string
	DiffBytes           int64
	WorkspaceAffinity   []string // project-relative path roots this workspace may write; empty = no affinity configured
	ModifiesOwners      bool
	EnabledCapabilities []string
	IsLandRequest       bool
	IsProjectCreate     bool
}

// PolicyViolation mirrors the shape of
// docs/spec/mcp-tools/common.schema.json#/$defs/Error, scoped to policy
// rejections (code/message/suggestion; no retryable - a policy violation is
// never retryable unresolved).
type PolicyViolation struct {
	Code       string
	Message    string
	Suggestion string
}

// EvaluatePolicy enforces AgentPolicy against a push - server-side, since
// that's the only enforcement that counts (§8.4, §15.3: "never trust
// client-claimed affinity alone"). It only applies to agent principals;
// human pushes are governed by owners/branch protection, not AgentPolicy.
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
		for _, f := range summary.ChangedFiles {
			if !withinAffinity(f, summary.WorkspaceAffinity) {
				v = append(v, PolicyViolation{
					Code:       "path_outside_affinity",
					Message:    fmt.Sprintf("%s is outside this workspace's project affinity", f),
					Suggestion: "expand workspace affinity or open a new workspace for this path",
				})
			}
		}
	}

	if policy.MaxChangedFiles > 0 && len(summary.ChangedFiles) > policy.MaxChangedFiles {
		v = append(v, PolicyViolation{
			Code:    "max_changed_files_exceeded",
			Message: fmt.Sprintf("changed %d files, agent policy allows at most %d", len(summary.ChangedFiles), policy.MaxChangedFiles),
		})
	}

	if policy.MaxDiffBytes > 0 && summary.DiffBytes > policy.MaxDiffBytes {
		v = append(v, PolicyViolation{
			Code:    "max_diff_bytes_exceeded",
			Message: fmt.Sprintf("diff is %d bytes, agent policy allows at most %d", summary.DiffBytes, policy.MaxDiffBytes),
		})
	}

	for _, f := range summary.ChangedFiles {
		if pattern, matched := matchAny(policy.DenylistPaths, f); matched {
			v = append(v, PolicyViolation{
				Code:    "denylist_path",
				Message: fmt.Sprintf("%s matches denylisted pattern %q for agent writes", f, pattern),
			})
		}
	}

	if summary.ModifiesOwners && !policy.CanModifyOwners {
		v = append(v, PolicyViolation{
			Code:    "owners_modification_denied",
			Message: "this agent policy does not allow modifying owners",
		})
	}

	if summary.IsProjectCreate && !policy.CanCreateProjects {
		v = append(v, PolicyViolation{
			Code:    "project_create_denied",
			Message: "this agent policy does not allow creating projects",
		})
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
			})
		}
	}

	return v
}

func withinAffinity(changedPath string, affinity []string) bool {
	for _, root := range affinity {
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
