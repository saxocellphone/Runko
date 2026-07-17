package receive

import (
	"strings"
	"testing"
)

func violationCodes(v []PolicyViolation) map[string]bool {
	out := make(map[string]bool, len(v))
	for _, x := range v {
		out[x.Code] = true
	}
	return out
}

// TestDefaultAgentPolicyRequiresDescription pins §8.7's default: agent
// changes must carry a §8.6 description, enforced as a merge gate in runkod's
// mergeRequirements (never at receive - the blurb is set after the push).
func TestDefaultAgentPolicyRequiresDescription(t *testing.T) {
	if !DefaultAgentPolicy().RequireDescription {
		t.Fatal("the default agent policy must require a description (§8.7 gate on §8.6)")
	}
}

func TestEvaluatePolicySatisfied(t *testing.T) {
	policy := DefaultAgentPolicy()
	summary := PushSummary{
		ChangedFiles:      []string{"commerce/checkout/handler.go"},
		DiffBytes:         100,
		WorkspaceAffinity: []string{"commerce/checkout"},
	}
	if v := EvaluatePolicy(policy, summary); len(v) != 0 {
		t.Fatalf("expected no violations, got %+v", v)
	}
}

// TestEvaluatePolicyRootAffinityGrantsWholeTree pins migration finding
// #40: the ROOT project's path is "" (it owns what no deeper manifest
// claims), so a workspace with root affinity carries [""] as its write
// allowlist - which the prefix arithmetic could never match (no path
// starts with "/"), write-blocking every agent granted root affinity.
// TestAffinityRejectionNamesPathAndSet (FIX #5): the old bare "%s is outside
// this workspace's project affinity" read as a directory even when it named a
// repo-root FILE (`runko-ci` looked like the `cli/runko-ci/` dir). The
// message now marks the path as a repo-root file and lists the affinity set
// by project name.
func TestAffinityRejectionNamesPathAndSet(t *testing.T) {
	v := EvaluatePolicy(AgentPolicy{RequireWorkspaceAffinity: true}, PushSummary{
		ChangedFiles:      []string{"runko-ci"},
		WorkspaceAffinity: []string{"cli", "docs", "platform", "runkod"},
		AffinityProjects:  []string{"cli", "docs", "platform", "runkod"},
	})
	if len(v) != 1 || v[0].Code != "path_outside_affinity" {
		t.Fatalf("want one path_outside_affinity, got %+v", v)
	}
	for _, want := range []string{`"runko-ci"`, "repo-root file", "{cli, docs, platform, runkod}"} {
		if !strings.Contains(v[0].Message, want) {
			t.Errorf("message %q missing %q", v[0].Message, want)
		}
	}
	// A path under a directory outside affinity names the directory.
	v = EvaluatePolicy(AgentPolicy{RequireWorkspaceAffinity: true}, PushSummary{
		ChangedFiles:      []string{"security/keys.txt"},
		WorkspaceAffinity: []string{"cli"},
		AffinityProjects:  []string{"cli"},
	})
	if len(v) != 1 || !strings.Contains(v[0].Message, `under directory "security/"`) {
		t.Fatalf("want the directory named, got %+v", v)
	}
	// With no project names supplied, the set falls back to the path roots
	// (root project's "" rendered as <repo root>).
	v = EvaluatePolicy(AgentPolicy{RequireWorkspaceAffinity: true}, PushSummary{
		ChangedFiles:      []string{"cli/x.go"},
		WorkspaceAffinity: []string{"docs"},
	})
	if len(v) != 1 || !strings.Contains(v[0].Message, "{docs}") {
		t.Fatalf("want path-root fallback set, got %+v", v)
	}
}

func TestEvaluatePolicyRootAffinityGrantsWholeTree(t *testing.T) {
	policy := DefaultAgentPolicy()
	summary := PushSummary{
		ChangedFiles:      []string{"AGENTS.md", "docs/design.md", "deep/nested/file.go"},
		DiffBytes:         100,
		WorkspaceAffinity: []string{""},
	}
	if v := EvaluatePolicy(policy, summary); len(v) != 0 {
		t.Fatalf("root affinity must admit any path, got %+v", v)
	}
}

func TestEvaluatePolicyTable(t *testing.T) {
	cases := []struct {
		name    string
		policy  AgentPolicy
		summary PushSummary
		want    string // violation code expected to be present
	}{
		{
			name:    "workspace affinity required but missing",
			policy:  AgentPolicy{RequireWorkspaceAffinity: true},
			summary: PushSummary{ChangedFiles: []string{"a.go"}},
			want:    "workspace_affinity_required",
		},
		{
			name:   "path outside affinity",
			policy: AgentPolicy{},
			summary: PushSummary{
				ChangedFiles:      []string{"other/project/file.go"},
				WorkspaceAffinity: []string{"commerce/checkout"},
			},
			want: "path_outside_affinity",
		},
		{
			name:   "max changed files exceeded",
			policy: AgentPolicy{MaxChangedFiles: 1},
			summary: PushSummary{
				ChangedFiles: []string{"a.go", "b.go"},
			},
			want: "max_changed_files_exceeded",
		},
		{
			name:   "max diff bytes exceeded",
			policy: AgentPolicy{MaxDiffBytes: 100},
			summary: PushSummary{
				ChangedFiles: []string{"a.go"},
				DiffBytes:    1000,
			},
			want: "max_diff_bytes_exceeded",
		},
		{
			name:   "denylisted path",
			policy: AgentPolicy{DenylistPaths: []string{"security/**"}},
			summary: PushSummary{
				ChangedFiles: []string{"security/secrets.go"},
			},
			want: "denylist_path",
		},
		{
			name:   "owners modification denied",
			policy: AgentPolicy{CanModifyOwners: false},
			summary: PushSummary{
				ChangedFiles:   []string{"OWNERS"},
				ModifiesOwners: true,
			},
			want: "owners_modification_denied",
		},
		{
			name:   "project create denied",
			policy: AgentPolicy{CanCreateProjects: false},
			summary: PushSummary{
				ChangedFiles:    []string{"newproj/PROJECT.yaml"},
				IsProjectCreate: true,
			},
			want: "project_create_denied",
		},
		{
			name:   "owner self grant denied",
			policy: AgentPolicy{CanCreateProjects: true},
			summary: PushSummary{
				ChangedFiles:     []string{"newproj/PROJECT.yaml"},
				IsProjectCreate:  true,
				Author:           "agent-task-1",
				NewProjectOwners: []string{"agent-task-1"},
			},
			want: "owner_self_grant",
		},
		{
			name:   "land denied by default",
			policy: AgentPolicy{CanLandChanges: false},
			summary: PushSummary{
				IsLandRequest: true,
			},
			want: "land_denied",
		},
		{
			name:   "capability not allowed",
			policy: AgentPolicy{CanEnableCapabilities: []string{"http"}},
			summary: PushSummary{
				EnabledCapabilities: []string{"deploy"},
			},
			want: "capability_denied",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := EvaluatePolicy(tc.policy, tc.summary)
			if !violationCodes(v)[tc.want] {
				t.Fatalf("expected violation %q, got %+v", tc.want, v)
			}
		})
	}
}

func TestEvaluatePolicyAllowedCapabilityProducesNoViolation(t *testing.T) {
	policy := AgentPolicy{CanEnableCapabilities: []string{"http", "rpc"}}
	v := EvaluatePolicy(policy, PushSummary{EnabledCapabilities: []string{"http"}})
	if len(v) != 0 {
		t.Fatalf("expected no violations for an allowed capability, got %+v", v)
	}
}

func TestEvaluatePolicyLandAllowedWhenPolicyPermits(t *testing.T) {
	policy := AgentPolicy{CanLandChanges: true}
	v := EvaluatePolicy(policy, PushSummary{IsLandRequest: true})
	if len(v) != 0 {
		t.Fatalf("expected no violations when CanLandChanges=true, got %+v", v)
	}
}

// TestEvaluatePolicyProjectCreateNamingOthersPasses is finding 2 of the
// 2026-07-16 dogfood review, at the policy layer: a create that names the
// minting human (not the agent) violates nothing under the default policy.
func TestEvaluatePolicyProjectCreateNamingOthersPasses(t *testing.T) {
	v := EvaluatePolicy(DefaultAgentPolicy(), PushSummary{
		ChangedFiles:      []string{"services/newproj/PROJECT.yaml", "services/newproj/main.go"},
		WorkspaceAffinity: []string{"services"},
		IsProjectCreate:   true,
		Author:            "agent-task-1",
		NewProjectOwners:  []string{"alice"},
	})
	if len(v) != 0 {
		t.Fatalf("expected no violations for a create naming the minting human, got %+v", v)
	}
}
