package receive

import "testing"

func violationCodes(v []PolicyViolation) map[string]bool {
	out := make(map[string]bool, len(v))
	for _, x := range v {
		out[x.Code] = true
	}
	return out
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
