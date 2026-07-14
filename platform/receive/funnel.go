package receive

import (
	"fmt"

	"github.com/saxocellphone/runko/platform/contract"
)

// Principal identifies who is pushing. Human pushes are not governed by
// AgentPolicy (§8.1); Policy is only consulted when IsAgent is true.
type Principal struct {
	IsAgent bool
	Policy  AgentPolicy
}

// PushRequest is everything the funnel needs about one incoming push,
// whether it arrived via the magic-ref path or a workspace snapshot
// (§11.5's "one server-side funnel" for both).
type PushRequest struct {
	Ref           string // the ref the client pushed to, e.g. "refs/for/main" or "refs/heads/main"
	TrunkRef      string // the monorepo's trunk ref name, e.g. "main"
	CommitMessage string
	Files         []FileContent // full content of every changed file, for secret scanning
	ChangedPaths  []string
	DiffBytes     int64
	Principal     Principal

	WorkspaceAffinity   []string
	ModifiesOwners      bool
	EnabledCapabilities []string
	IsLandRequest       bool
	IsProjectCreate     bool

	// ChangeIDSeed is seed material (e.g. tree SHA + author + timestamp) for
	// a fresh Change-Id if CommitMessage doesn't already carry one.
	ChangeIDSeed string

	// ModulePath and Projects feed §13.3.1's contract checks
	// (platform/contract): the Go module path at the pushed head and the
	// head tree's indexed projects. Nil Projects skips the checks - the
	// caller opts each push shape in (change pushes yes; workspace
	// snapshots are WIP durability and are never contract-policed).
	ModulePath string
	Projects   []contract.Project
}

// Decision is the funnel's verdict. Exactly one of the rejection reasons is
// populated when Accepted is false: RejectionMessage (ref-level rejection,
// §6.9-style), PolicyViolations, SecretFindings, or ContractViolations.
type Decision struct {
	Accepted bool

	RejectionMessage   string
	PolicyViolations   []PolicyViolation
	SecretFindings     []SecretFinding
	ContractViolations []contract.Violation

	ChangeID      string
	CommitMessage string // possibly amended with a new Change-Id trailer
}

// Decide runs the receive funnel (§7.4, §11.5): ref check -> policy -> secret
// scan -> contract check (§13.3.1) -> Change-Id assignment. It does not
// update Git refs or Postgres rows
// itself - see CreateOrUpdateChange for the persistence half, kept separate
// so this decision logic stays pure and testable without a database (§28.2
// rule 3: no mocking git, no mocking Postgres either - keep the parts that
// don't need either fully isolated).
func Decide(req PushRequest, scanner SecretScanner) Decision {
	if IsDirectTrunkPush(req.Ref, req.TrunkRef) {
		return Decision{
			Accepted:         false,
			RejectionMessage: RejectDirectPush(req.TrunkRef, "https://runko.dev/docs/trunk"),
		}
	}

	if _, ok := ParseMagicRef(req.Ref); !ok {
		return Decision{
			Accepted:         false,
			RejectionMessage: fmt.Sprintf("remote: unrecognized ref %q - push to refs/for/%s instead\n", req.Ref, req.TrunkRef),
		}
	}

	if req.Principal.IsAgent {
		violations := EvaluatePolicy(req.Principal.Policy, PushSummary{
			ChangedFiles:        req.ChangedPaths,
			DiffBytes:           req.DiffBytes,
			WorkspaceAffinity:   req.WorkspaceAffinity,
			ModifiesOwners:      req.ModifiesOwners,
			EnabledCapabilities: req.EnabledCapabilities,
			IsLandRequest:       req.IsLandRequest,
			IsProjectCreate:     req.IsProjectCreate,
		})
		if len(violations) > 0 {
			return Decision{Accepted: false, PolicyViolations: violations}
		}
	}

	findings, err := scanner.Scan(req.Files)
	if err != nil {
		return Decision{Accepted: false, RejectionMessage: fmt.Sprintf("remote: secret scan failed: %v\n", err)}
	}
	if len(findings) > 0 {
		return Decision{Accepted: false, SecretFindings: findings}
	}

	if len(req.Projects) > 0 {
		files := make([]contract.File, len(req.Files))
		for i, f := range req.Files {
			files[i] = contract.File{Path: f.Path, Content: f.Content}
		}
		if violations := contract.Check(req.ModulePath, req.Projects, files, req.ChangedPaths); len(violations) > 0 {
			return Decision{Accepted: false, ContractViolations: violations}
		}
	}

	changeID, newMessage := EnsureChangeID(req.CommitMessage, req.ChangeIDSeed)
	return Decision{
		Accepted:      true,
		ChangeID:      changeID,
		CommitMessage: newMessage,
	}
}
