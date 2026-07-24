package receive

import (
	"errors"
	"testing"

	"github.com/saxocellphone/runko/platform/contract"
)

type fakeScanner struct {
	findings []SecretFinding
	err      error
}

func (f fakeScanner) Scan(_ []FileContent) ([]SecretFinding, error) { return f.findings, f.err }

func baseRequest() PushRequest {
	return PushRequest{
		Ref:           "refs/for/main",
		TrunkRef:      "main",
		CommitMessage: "Add feature",
		ChangedPaths:  []string{"commerce/checkout/handler.go"},
		DiffBytes:     100,
		Principal:     Principal{IsAgent: false},
		ChangeIDSeed:  "seed-abc",
	}
}

func TestDecideRejectsDirectTrunkPush(t *testing.T) {
	req := baseRequest()
	req.Ref = "refs/heads/main"
	d := Decide(req, NoOpScanner{})
	if d.Accepted {
		t.Fatalf("expected direct trunk push to be rejected")
	}
	if d.RejectionMessage == "" {
		t.Fatalf("expected a rejection message")
	}
}

func TestDecideRejectsUnrecognizedRef(t *testing.T) {
	req := baseRequest()
	req.Ref = "refs/tags/v1.0"
	d := Decide(req, NoOpScanner{})
	if d.Accepted {
		t.Fatalf("expected an unrecognized ref to be rejected")
	}
}

func TestDecideAcceptsMagicRefForHumanPrincipal(t *testing.T) {
	req := baseRequest()
	d := Decide(req, NoOpScanner{})
	if !d.Accepted {
		t.Fatalf("expected acceptance, got rejection: %+v", d)
	}
	if d.ChangeID == "" {
		t.Fatalf("expected a Change-Id to be assigned")
	}
}

func TestDecideEnforcesPolicyForAgentPrincipal(t *testing.T) {
	req := baseRequest()
	req.Principal = Principal{IsAgent: true, Policy: DefaultAgentPolicy()} // requires workspace affinity
	req.WorkspaceAffinity = nil

	d := Decide(req, NoOpScanner{})
	if d.Accepted {
		t.Fatalf("expected rejection due to missing workspace affinity")
	}
	if len(d.PolicyViolations) == 0 {
		t.Fatalf("expected policy violations to be populated")
	}
}

func TestDecideAcceptsCompliantAgentPush(t *testing.T) {
	req := baseRequest()
	req.Principal = Principal{IsAgent: true, Policy: DefaultAgentPolicy()}
	req.WorkspaceAffinity = []string{"commerce/checkout"}

	d := Decide(req, NoOpScanner{})
	if !d.Accepted {
		t.Fatalf("expected acceptance for a compliant agent push, got %+v", d)
	}
}

func TestDecideRejectsOnSecretFinding(t *testing.T) {
	req := baseRequest()
	scanner := fakeScanner{findings: []SecretFinding{{Path: "config.yaml", Line: 3, RuleID: "aws-key", Description: "looks like an AWS key"}}}

	d := Decide(req, scanner)
	if d.Accepted {
		t.Fatalf("expected rejection when the scanner reports a finding")
	}
	if len(d.SecretFindings) != 1 {
		t.Fatalf("expected 1 secret finding surfaced, got %d", len(d.SecretFindings))
	}
}

func TestDecideRejectsOnScannerError(t *testing.T) {
	req := baseRequest()
	scanner := fakeScanner{err: errors.New("scanner unavailable")}

	d := Decide(req, scanner)
	if d.Accepted {
		t.Fatalf("expected rejection when the scanner errors")
	}
	if d.RejectionMessage == "" {
		t.Fatalf("expected a rejection message describing the scanner failure")
	}
}

func TestDecidePreservesExistingChangeID(t *testing.T) {
	req := baseRequest()
	req.CommitMessage = "Add feature\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567\n"

	d := Decide(req, NoOpScanner{})
	if !d.Accepted {
		t.Fatalf("expected acceptance, got %+v", d)
	}
	if d.ChangeID != "I0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("expected the existing Change-Id to be preserved, got %s", d.ChangeID)
	}
}

// TestDecideContractCheck pins the funnel's fourth step (§13.3.1): with a
// project snapshot attached, an undeclared contract-gen import is refused
// with structured ContractViolations; the declared edge passes; and a
// request with NO Projects (workspace snapshots, unborn trunk) skips the
// check entirely.
func TestDecideContractCheck(t *testing.T) {
	projects := []contract.Project{
		{Name: "provider", Path: "provider", ContractGenDir: "provider/proto/gen"},
		{Name: "consumer", Path: "consumer"},
	}
	violating := FileContent{
		Path:    "consumer/client.go",
		Content: []byte("package consumer\n\nimport _ \"example.com/mono/provider/proto/gen/v1\"\n"),
	}

	req := baseRequest()
	req.ModulePath = "example.com/mono"
	req.Projects = projects
	req.Files = []FileContent{violating}
	req.ChangedPaths = []string{violating.Path}

	d := Decide(req, NoOpScanner{})
	if d.Accepted || len(d.ContractViolations) != 1 {
		t.Fatalf("want one contract violation, got accepted=%v %+v", d.Accepted, d.ContractViolations)
	}
	if d.ContractViolations[0].Code != "undeclared_contract_dependency" {
		t.Fatalf("unexpected code %q", d.ContractViolations[0].Code)
	}

	req.Projects[1].Dependencies = []string{"provider"}
	if d := Decide(req, NoOpScanner{}); !d.Accepted {
		t.Fatalf("declared edge must pass: %+v", d)
	}

	req.Projects = nil
	req.Files[0].Content = violating.Content
	if d := Decide(req, NoOpScanner{}); !d.Accepted {
		t.Fatalf("nil Projects must skip the contract check: %+v", d)
	}
}

// TestDecideAcceptsAckableViolations pins the 2026-07-24 enforcement split:
// a content-shaped finding (denylisted path, within affinity) no longer
// refuses - the decision is ACCEPTED and carries the finding for the caller
// to mint as the reserved agent-policy check. Hard findings still refuse.
func TestDecideAcceptsAckableViolations(t *testing.T) {
	req := baseRequest()
	req.Principal = Principal{IsAgent: true, Policy: DefaultAgentPolicy()}
	req.WorkspaceAffinity = []string{""} // root affinity: every path in scope
	req.ChangedPaths = []string{".github/workflows/ci.yml"}

	d := Decide(req, NoOpScanner{})
	if !d.Accepted {
		t.Fatalf("ackable-only violations must accept, got %+v", d)
	}
	if len(d.AckableViolations) != 1 || d.AckableViolations[0].Code != "denylist_path" {
		t.Fatalf("accepted decision must carry the ackable finding, got %+v", d.AckableViolations)
	}
	if d.ChangeID == "" {
		t.Fatalf("accepted decision must still mint a Change-Id")
	}

	// Hard + ackable together: the hard one refuses, and the refusal names
	// ONLY the hard class (the ackable finding is not a refusal reason).
	req2 := baseRequest()
	req2.Principal = Principal{IsAgent: true, Policy: DefaultAgentPolicy()}
	req2.WorkspaceAffinity = nil // hard: affinity required
	req2.ChangedPaths = []string{".github/workflows/ci.yml"}
	d2 := Decide(req2, NoOpScanner{})
	if d2.Accepted {
		t.Fatalf("hard violation must still refuse")
	}
	for _, v := range d2.PolicyViolations {
		if v.Ackable {
			t.Fatalf("refusal must carry only hard violations, got %+v", d2.PolicyViolations)
		}
	}
}

// TestSplitAckableClasses pins which codes belong to which enforcement
// class - moving one is a policy decision, not a refactor.
func TestSplitAckableClasses(t *testing.T) {
	policy := DefaultAgentPolicy()
	policy.CanModifyOwners = false
	policy.CanCreateProjects = false
	policy.MaxChangedFiles = 1
	policy.MaxDiffBytes = 1

	v := EvaluatePolicy(policy, PushSummary{
		ChangedFiles:        []string{"a.go", ".github/workflows/x.yml"},
		DiffBytes:           2,
		WorkspaceAffinity:   nil, // hard
		ModifiesOwners:      true,
		IsLandRequest:       true, // hard
		EnabledCapabilities: []string{"data_store"},
	})
	class := map[string]bool{} // code -> ackable
	for _, x := range v {
		class[x.Code] = x.Ackable
	}
	for code, wantAckable := range map[string]bool{
		"workspace_affinity_required": false,
		"land_denied":                 false,
		"denylist_path":               true,
		"max_changed_files_exceeded":  true,
		"max_diff_bytes_exceeded":     true,
		"owners_modification_denied":  true,
		"capability_denied":           true,
	} {
		got, present := class[code]
		if !present {
			t.Fatalf("expected violation %s, got %+v", code, v)
		}
		if got != wantAckable {
			t.Fatalf("%s: ackable=%v, want %v", code, got, wantAckable)
		}
	}
}
