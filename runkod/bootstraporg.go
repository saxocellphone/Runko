// Org governance bootstrap - the §6.10 genesis, retrofitted. Orgs created
// before genesis existed (or imported bare) sit in a deadlock under
// default-deny: no OWNERS anywhere means no owner requirement resolves, no
// ci.checks means no required checks, so the merge gate refuses every land
// as unpoliced - including the very change that would add the missing
// OWNERS, when the operator's only documented escape was a server restart
// flag (--insecure-allow-unpoliced-land). `runko org bootstrap` is the
// one-command exit: a server-authored Change (deleteproject.go's
// mechanics) adding the governance minimum - root OWNERS naming the
// caller, plus the root manifest when none exists. Owners resolve from the
// change's own head tree (api.go computeAffected) and a human owner-author
// self-satisfies by uploader consent (§6.10), so the caller lands it
// immediately - while the bootstrap itself stays ordinary, reviewable,
// revertable history. Humans only; where org roles exist, org admins only:
// the bootstrap decides who governs from then on.
package runkod

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/receive"
)

// BootstrapOutcome is the wire result of POST /api/org/bootstrap: either a
// governance Change to land (born trunk) or a directly seeded genesis
// (unborn trunk - there is no history to review against, the same standing
// as org creation).
type BootstrapOutcome struct {
	SeededGenesis bool
	Change        Change
}

// bootstrapAdminGate refuses store-backed accounts that are not admins of
// this org. Org roles exist only under a directory-run hub; operator/flag
// principals are server-wide grants and pass - the same split auth.go's
// membership gate draws.
func (s *Server) bootstrapAdminGate(ctx context.Context, p *Principal) *apiError {
	if !p.Stored || s.Directory == nil || s.OrgName == "" {
		return nil
	}
	role, member, err := s.Directory.OrgMemberRole(ctx, s.OrgName, p.Name)
	if err != nil {
		return internalErr(fmt.Errorf("resolve org role: %w", err))
	}
	if !member || role != "admin" {
		return typedErr(http.StatusForbidden, clierr.Error{
			Code: "not_org_admin", Field: "auth",
			Message:    "org bootstrap decides who governs this org from then on - org admins only",
			Suggestion: "ask an org admin to run `runko org bootstrap`, or have one grant you admin: runko org add-member --org " + s.OrgName + " --name <you> --role admin",
		})
	}
	return nil
}

// bootstrapOrgCore is the whole verb: gate the caller, then either seed an
// unborn trunk directly or open the governance Change against the born one.
func (s *Server) bootstrapOrgCore(ctx context.Context, principal *Principal) (BootstrapOutcome, *apiError) {
	if principal == nil {
		return BootstrapOutcome{}, typedErr(http.StatusForbidden, clierr.Error{
			Code: "bootstrap_needs_identity", Field: "auth",
			Message:    "org bootstrap records its caller as the org's first owner - the anonymous deploy token has no name to record",
			Suggestion: "sign in as yourself first: runko auth login --runkod-url <url>/o/<org> --name <you>",
		})
	}
	if principal.IsAgent {
		return BootstrapOutcome{}, typedErr(http.StatusForbidden, clierr.Error{
			Code: "agents_cannot_bootstrap_org", Field: "auth",
			Message:    "bootstrapping org governance is a human product action: it names who owns the tree",
			Suggestion: "ask a human to run `runko org bootstrap`",
		})
	}
	if apiErr := s.bootstrapAdminGate(ctx, principal); apiErr != nil {
		return BootstrapOutcome{}, apiErr
	}

	g := gitstore.New(s.RepoDir)
	tip, err := g.ResolveRef("refs/heads/" + s.TrunkRef)
	if err != nil {
		// Unborn trunk: nothing ever landed, so there is no history to
		// review against and no reviewer to ask - seed the full genesis
		// directly, exactly as org creation would have (§6.10).
		if err := seedGenesisCommit(s.RepoDir, s.TrunkRef, s.orgDisplayName(), principal.Name); err != nil {
			return BootstrapOutcome{}, internalErr(fmt.Errorf("seed genesis: %w", err))
		}
		return BootstrapOutcome{SeededGenesis: true}, nil
	}

	indexed, err := s.indexedProjectsAt(g, tip)
	if err != nil {
		return BootstrapOutcome{}, internalErr(fmt.Errorf("scan trunk projects: %w", err))
	}
	hasRootManifest := false
	for _, p := range indexed {
		if len(p.Owners) > 0 {
			return BootstrapOutcome{}, typedErr(http.StatusConflict, clierr.Error{
				Code: "already_governed", Field: "org",
				Message:    fmt.Sprintf("owners already resolve at trunk (project %s) - bootstrap is only for an ownerless org", p.Name),
				Suggestion: "evolve ownership with an ordinary change to OWNERS or a manifest's owners: field",
			})
		}
		if p.Path == "" || p.Path == "." {
			hasRootManifest = true
		}
	}

	// The governance minimum only: root OWNERS naming the caller, and the
	// root manifest when no project owns the root (without it, paths
	// outside every project resolve no owning project and no policy).
	// The tree's own genesis content, single-sourced from genesisFiles -
	// the org keeps evolving both files through ordinary changes.
	var overlay core.Overlay
	var paths []string
	for _, f := range genesisFiles(s.orgDisplayName(), principal.Name, s.TrunkRef) {
		switch f.Path {
		case "OWNERS":
		case "PROJECT.yaml":
			if hasRootManifest {
				continue
			}
		default:
			continue
		}
		overlay.Changes = append(overlay.Changes, f)
		paths = append(paths, f.Path)
	}

	baseSHA := string(tip)
	changeID := receive.GenerateChangeID("org-bootstrap|" + baseSHA + "|" + time.Now().UTC().String())
	title := fmt.Sprintf("Bootstrap org governance: root OWNERS (%s)", principal.Name)
	msg := title + "\n\n" +
		"Seeded by `runko org bootstrap` (governance retrofit): this org's trunk\n" +
		"resolved no owners anywhere, so under default-deny nothing could land -\n" +
		"including this fix. Owners resolve from this change's own head tree and\n" +
		"the author's push is their consent (uploader model), which is what makes\n" +
		"this one change self-landable.\n\nChange-Id: " + changeID + "\n"

	rev, err := g.CommitOverlay(tip, overlay, core.CommitMeta{
		AuthorName: principal.Name, AuthorEmail: principal.Name + "@runko", Message: msg,
	})
	if err != nil {
		return BootstrapOutcome{}, internalErr(fmt.Errorf("commit bootstrap overlay: %w", err))
	}
	changeRef := "refs/changes/" + changeID + "/head"
	if err := g.UpdateRef(changeRef, rev, nil); err != nil {
		return BootstrapOutcome{}, internalErr(fmt.Errorf("write change ref: %w", err))
	}
	change, err := s.Store.CreateOrUpdateChange(ctx, changeID, baseSHA, string(rev), changeRef, title, principal.Name, "", "")
	if err != nil {
		return BootstrapOutcome{}, internalErr(fmt.Errorf("record change: %w", err))
	}
	if s.Processor != nil {
		s.Processor.computeAffectedAndEnqueue(ctx, change, paths, nil)
	}
	return BootstrapOutcome{Change: change}, nil
}

// orgDisplayName names this org in generated genesis content; the
// root-mounted default server has no org name and reads as the repo itself.
func (s *Server) orgDisplayName() string {
	if s.OrgName != "" {
		return s.OrgName
	}
	return "this monorepo"
}

func (s *Server) handleBootstrapOrg(w http.ResponseWriter, r *http.Request) {
	out, apiErr := s.bootstrapOrgCore(r.Context(), s.principalFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"seeded_genesis": out.SeededGenesis,
		"change_id":      out.Change.ChangeKey,
		"title":          out.Change.Title,
	})
}
