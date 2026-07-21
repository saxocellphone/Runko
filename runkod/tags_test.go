package runkod

import (
	"context"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/platform/receive"
)

const tagTestSHA = "1111111111111111111111111111111111111111"

// newTagTestProcessor builds a Processor over a MemStore acting as its own
// settings directory (the default-org shape: Store doubles as Directory),
// with one admin-role member, one releaser, one plain member, and a bot
// lane scoped to commerce/checkout-api/v*.
func newTagTestProcessor(t *testing.T) (*Processor, *MemStore) {
	t.Helper()
	mem := NewMemStore()
	ctx := context.Background()
	if err := mem.EnsureOrg(ctx, "test-org"); err != nil {
		t.Fatalf("EnsureOrg: %v", err)
	}
	for name, role := range map[string]string{"alice": "admin", "rel": "releaser", "bob": "member"} {
		if err := mem.CreatePrincipal(ctx, "test-org", name, "hash", ""); err != nil {
			t.Fatalf("CreatePrincipal(%s): %v", name, err)
		}
		if err := mem.UpsertOrgMember(ctx, "test-org", name, role); err != nil {
			t.Fatalf("UpsertOrgMember(%s): %v", name, err)
		}
	}
	proc := &Processor{
		RepoDir: newBareRepo(t), TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: mem,
		OrgName: "test-org",
		Principals: []Principal{
			{Name: "op", Token: "optok", Admin: true},
		},
		BotLanes: []BotLane{{
			Name: "relbot", Token: "lanetok",
			PathAllowlist:  []string{"deploy/**"},
			RequiredChecks: []string{"manifest-lint"},
			TagAllowlist:   []string{"commerce/checkout-api/v*"},
		}},
	}
	return proc, mem
}

func pushTag(t *testing.T, proc *Processor, ref string, extraEnv []string) RefResult {
	t.Helper()
	return proc.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: tagTestSHA, Ref: ref}, extraEnv)
}

// TestTagPolicyPermissiveByDefault pins §14.10.3's rollout rule: with the
// knob off (the default - nothing set on the org), every tag push is
// accepted unconditionally, exactly the documented v1 permissiveness.
func TestTagPolicyPermissiveByDefault(t *testing.T) {
	proc, _ := newTagTestProcessor(t)
	for _, env := range [][]string{nil, {"REMOTE_USER=bob"}, {"REMOTE_USER=ghost"}, {"REMOTE_LANE=relbot"}} {
		if res := pushTag(t, proc, "refs/tags/anything/v1", env); !res.Accepted {
			t.Fatalf("knob off: expected unconditional accept for env %v, got %+v", env, res)
		}
	}
}

// TestTagPolicyEnforced is the stage-17 gate table: operator credential,
// admin principal, admin/releaser org roles, and in-namespace lanes write
// tags; plain members, unknown names, and out-of-namespace lanes are
// refused with the §6.9-style script. Deletes take the same gate.
func TestTagPolicyEnforced(t *testing.T) {
	proc, mem := newTagTestProcessor(t)
	if err := mem.UpdateOrgSettings(context.Background(), "test-org", OrgSettings{EnforceTagPolicy: true}); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}

	allowed := []struct {
		name string
		env  []string
	}{
		{"anonymous deploy token", nil},
		{"admin config principal", []string{"REMOTE_USER=op"}},
		{"admin org role", []string{"REMOTE_USER=alice"}},
		{"releaser org role", []string{"REMOTE_USER=rel"}},
		{"lane inside its namespace", []string{"REMOTE_LANE=relbot"}},
	}
	for _, tc := range allowed {
		ref := "refs/tags/commerce/checkout-api/v1.2.3"
		if res := pushTag(t, proc, ref, tc.env); !res.Accepted {
			t.Fatalf("%s: expected accept, got %+v", tc.name, res)
		}
	}

	denied := []struct {
		name string
		ref  string
		env  []string
		want string
	}{
		{"plain member", "refs/tags/v1", []string{"REMOTE_USER=bob"}, "releaser"},
		{"unknown principal", "refs/tags/v1", []string{"REMOTE_USER=ghost"}, "releaser"},
		{"lane outside its namespace", "refs/tags/platform/authz/v1", []string{"REMOTE_LANE=relbot"}, "tag namespaces"},
		{"unknown lane", "refs/tags/commerce/checkout-api/v9", []string{"REMOTE_LANE=ghostlane"}, "tag namespaces"},
	}
	for _, tc := range denied {
		res := pushTag(t, proc, tc.ref, tc.env)
		if res.Accepted {
			t.Fatalf("%s: expected refusal, got accept", tc.name)
		}
		if !strings.Contains(res.Message, "remote:") || !strings.Contains(res.Message, tc.want) {
			t.Fatalf("%s: expected a scripted rejection mentioning %q, got %q", tc.name, tc.want, res.Message)
		}
	}

	// A tag DELETE by an unauthorized pusher is refused by the same gate -
	// a deleted release tag corrupts CD exactly like a forged one.
	res := proc.Process(context.Background(),
		RefUpdate{OldSHA: tagTestSHA, NewSHA: zeroOID, Ref: "refs/tags/v1"}, []string{"REMOTE_USER=bob"})
	if res.Accepted {
		t.Fatalf("expected a member's tag delete to be refused under enforcement")
	}
}

// TestTagPolicyGateIsPerOrg: the knob lives on org settings - a second
// org's processor sharing the registries but naming an org with no
// settings row stays permissive.
func TestTagPolicyGateIsPerOrg(t *testing.T) {
	proc, mem := newTagTestProcessor(t)
	if err := mem.UpdateOrgSettings(context.Background(), "test-org", OrgSettings{EnforceTagPolicy: true}); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}
	other := &Processor{
		RepoDir: proc.RepoDir, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: mem,
		OrgName: "other-org",
	}
	if res := pushTag(t, other, "refs/tags/v1", []string{"REMOTE_USER=bob"}); !res.Accepted {
		t.Fatalf("other org has no enforcement - expected accept, got %+v", res)
	}
}
