package runkod

import (
	"context"
	"testing"

	"github.com/saxocellphone/runko/platform/receive"
)

// TestAgentPolicyForResolvesOverride pins the single-resolution-point contract
// (§8.7): absent an override an org gets the safe DefaultAgentPolicy(); a stored
// override wins; a delete restores the default; and a nil directory fails safe
// to the default (never to a zero-value all-permissive policy).
func TestAgentPolicyForResolvesOverride(t *testing.T) {
	ctx := context.Background()
	mem := NewMemStore()

	if def := agentPolicyFor(ctx, mem, "org"); len(def.DenylistPaths) == 0 || def.CanModifyOwners {
		t.Fatalf("absent override must yield the locked-down default, got %+v", def)
	}

	loose := receive.DefaultAgentPolicy()
	loose.DenylistPaths = nil
	loose.CanModifyOwners = true
	if err := mem.SetAgentPolicy(ctx, "org", "", loose, "operator"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := agentPolicyFor(ctx, mem, "org"); len(got.DenylistPaths) != 0 || !got.CanModifyOwners {
		t.Fatalf("stored override not applied, got %+v", got)
	}
	if p, ok, _ := mem.GetAgentPolicy(ctx, "org", ""); !ok || !p.CanModifyOwners {
		t.Fatalf("round-trip failed: ok=%v p=%+v", ok, p)
	}

	if err := mem.DeleteAgentPolicy(ctx, "org", ""); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if p := agentPolicyFor(ctx, mem, "org"); len(p.DenylistPaths) == 0 {
		t.Fatalf("after delete must be back to default, got %+v", p)
	}

	// A different org with no override is unaffected by the first org's policy.
	if p := agentPolicyFor(ctx, mem, "other"); len(p.DenylistPaths) == 0 {
		t.Fatalf("a fresh org must stay locked down, got %+v", p)
	}
	// nil directory -> fail safe to default.
	if p := agentPolicyFor(ctx, nil, "org"); len(p.DenylistPaths) == 0 {
		t.Fatalf("nil directory must fail safe to the default")
	}
}
