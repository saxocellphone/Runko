// Server-side project creation (§10.1's intent -> files -> preview ->
// apply pipeline, §8.5's create_project semantics): the decision core
// behind ProjectService.PreviewCreateProject / CreateProject.
//
// Trunk is closed to direct writes (§6.9), so "creating a project" never
// touches trunk: Apply builds one commit object on top of the trunk tip
// and the result is registered as an ordinary open Change - the project
// becomes real by LANDING that Change through the normal §13.5 gates,
// and abandoning it leaves no trace. The CLI path (`runko project create`
// + `runko change push`) produces exactly the same shape through the
// receive funnel; this is its remote-client equivalent for the web UI
// (the MCP catalog keeps the tool itself deferred-v1.x, §8.3).
package runkod

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/project"
	"github.com/saxocellphone/runko/platform/receive"
)

// previewProjectCore runs PlanCreate without applying: the §10.1 preview
// step, plus the collision check against trunk's current project index
// (tree-as-truth, §10.3 - the check is against what the tree holds NOW;
// two racing creates resolve at land time like any other conflict).
func (s *Server) previewProjectCore(intent project.Intent) (project.Plan, *apiError) {
	plan, verrs := project.PlanCreate(intent, project.DefaultTemplates())
	if len(verrs) > 0 {
		v := verrs[0]
		return project.Plan{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code:       v.Code,
			Field:      v.Field,
			Message:    v.Message,
			Suggestion: v.Suggestion,
		})
	}
	existing, err := s.trunkProjects()
	if err != nil {
		return project.Plan{}, internalErr(fmt.Errorf("scan trunk projects: %w", err))
	}
	for _, p := range existing {
		if p.Name == plan.EffectiveManifest.Name || p.Path == plan.Path {
			return project.Plan{}, typedErr(http.StatusConflict, clierr.Error{
				Code:       "already_exists",
				Field:      "intent.name",
				Message:    fmt.Sprintf("project %s already exists at %s", p.Name, p.Path),
				Suggestion: "pick a different name, or evolve the existing project with an ordinary change",
			})
		}
	}
	return plan, nil
}

// createProjectCore applies the plan as a single commit object (no ref on
// trunk moves) and registers it as an open Change under the stable
// refs/changes/<id>/head ref, exactly the shape the receive funnel gives a
// pushed Change (§14.4.4). An unborn trunk is fine: the Change carries the
// repo's first-ever commit and land.Land's bootstrap CAS handles the rest
// (§28.3 stage 11b).
func (s *Server) createProjectCore(ctx context.Context, intent project.Intent, principal *Principal) (Change, *apiError) {
	plan, apiErr := s.previewProjectCore(intent)
	if apiErr != nil {
		return Change{}, apiErr
	}

	g := gitstore.New(s.RepoDir)
	baseSHA := ""
	if tip, err := g.ResolveRef("refs/heads/" + s.TrunkRef); err == nil {
		baseSHA = string(tip)
	}

	// Attribution mirrors the funnel (§15.1 interim): a signed-in principal
	// authors as itself; the anonymous deploy token authors as "" with a
	// neutral git identity, same as an anonymous push.
	authorName, authorEmail, authoredBy := "runko-web", "runko-web@localhost", ""
	if principal != nil {
		authorName, authorEmail, authoredBy = principal.Name, principal.Name+"@runko", principal.Name
	}

	changeID := receive.GenerateChangeID(plan.Path + "|" + baseSHA + "|" + time.Now().UTC().String())
	title := fmt.Sprintf("Create project %s", plan.EffectiveManifest.Name)
	// The Change-Id trailer rides in the commit message so a later
	// `git fetch <ref>` + amend + push amends THIS Change, not a new one.
	msg := fmt.Sprintf("%s\n\nChange-Id: %s\n", title, changeID)

	rev, err := project.Apply(g, core.Revision(baseSHA), plan, core.CommitMeta{
		AuthorName:  authorName,
		AuthorEmail: authorEmail,
		Message:     msg,
	})
	if err != nil {
		return Change{}, internalErr(fmt.Errorf("apply project plan: %w", err))
	}

	changeRef := "refs/changes/" + changeID + "/head"
	if err := g.UpdateRef(changeRef, rev, nil); err != nil {
		return Change{}, internalErr(fmt.Errorf("write change ref: %w", err))
	}

	// No origin workspace: UI/RPC project creation is a workspace-less
	// write path by design (§12.2 provenance is advisory, not required).
	change, err := s.Store.CreateOrUpdateChange(ctx, changeID, baseSHA, string(rev), changeRef, title, authoredBy, "", "")
	if err != nil {
		return Change{}, internalErr(fmt.Errorf("record change: %w", err))
	}

	// Same post-accept side effects as a funnel-accepted push: affected
	// computation + change webhook for CI (§14.4.1).
	if s.Processor != nil {
		paths := make([]string, len(plan.Files))
		for i, f := range plan.Files {
			paths[i] = path.Join(plan.Path, f.Path)
		}
		s.Processor.computeAffectedAndEnqueue(ctx, change, paths, nil)
	}

	return change, nil
}
