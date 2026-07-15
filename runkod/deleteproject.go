// Project deletion - create's dual (§13.1, decided 2026-07-15): the same
// server-authored-Change mechanics as createproject.go, inverted. The plan
// (platform/project.PlanDelete) removes the project's whole subtree and
// line-surgically strips its name from every other manifest's
// dependencies:/consumes:, so no dangling edge survives; the daemon
// commits it and registers an ordinary open Change that lands through the
// normal gates - deleting a project is reviewable and revertable. Agents
// are refused at this surface (§8.7 posture): a human clicks the project
// page's delete button or types `runko project delete`.
package runkod

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/project"
	"github.com/saxocellphone/runko/platform/receive"
)

// previewDeleteProjectCore builds the plan against the trunk tip.
func (s *Server) previewDeleteProjectCore(name string) (project.DeletePlan, *apiError) {
	g := gitstore.New(s.RepoDir)
	tip, err := g.ResolveRef("refs/heads/" + s.TrunkRef)
	if err != nil {
		return project.DeletePlan{}, typedErr(http.StatusNotFound, clierr.Error{
			Code: "unknown_project", Field: "name",
			Message:    "the monorepo has no trunk yet - nothing to delete",
			Suggestion: "runko project list shows every project",
		})
	}
	indexed, err := s.indexedProjectsAt(g, core.Revision(tip))
	if err != nil {
		return project.DeletePlan{}, internalErr(fmt.Errorf("scan trunk projects: %w", err))
	}

	targets := make([]project.DeleteTarget, len(indexed))
	var targetPath string
	for i, p := range indexed {
		targets[i] = project.DeleteTarget{Name: p.Name, Path: p.Path}
		if p.Name == name {
			targetPath = p.Path
		}
	}

	var files []string
	if targetPath != "" && targetPath != "." {
		files, err = listSubtree(g, core.Revision(tip), targetPath)
		if err != nil {
			return project.DeletePlan{}, internalErr(fmt.Errorf("list %s at trunk: %w", targetPath, err))
		}
	}

	var manifests []project.ManifestRef
	for _, p := range indexed {
		if p.Name == name {
			continue
		}
		mPath := path.Join(p.Path, "PROJECT.yaml")
		if p.Path == "" || p.Path == "." {
			mPath = "PROJECT.yaml"
		}
		blob, err := g.GetBlob(core.Revision(tip), mPath)
		if err != nil {
			return project.DeletePlan{}, internalErr(fmt.Errorf("read %s: %w", mPath, err))
		}
		manifests = append(manifests, project.ManifestRef{Path: mPath, Content: blob.Content})
	}

	plan, verrs := project.PlanDelete(name, targets, files, manifests)
	if len(verrs) > 0 {
		v := verrs[0]
		status := http.StatusBadRequest
		if v.Code == "unknown_project" {
			status = http.StatusNotFound
		}
		return project.DeletePlan{}, typedErr(status, clierr.Error{
			Code: v.Code, Field: v.Field, Message: v.Message, Suggestion: v.Suggestion,
		})
	}
	return plan, nil
}

// deleteProjectCore applies the plan exactly like createProjectCore: one
// commit object, a stable change ref, an open Change row, and the same
// post-accept side effects (affected computation + webhook).
func (s *Server) deleteProjectCore(ctx context.Context, name string, principal *Principal) (Change, *apiError) {
	if principal != nil && principal.IsAgent {
		return Change{}, typedErr(http.StatusForbidden, clierr.Error{
			Code: "agents_cannot_delete_projects", Field: "auth",
			Message:    "deleting a project is a human product action (§13.1)",
			Suggestion: "ask a human to run `runko project delete` or use the project page",
		})
	}

	plan, apiErr := s.previewDeleteProjectCore(name)
	if apiErr != nil {
		return Change{}, apiErr
	}

	g := gitstore.New(s.RepoDir)
	tip, err := g.ResolveRef("refs/heads/" + s.TrunkRef)
	if err != nil {
		return Change{}, internalErr(fmt.Errorf("resolve trunk: %w", err))
	}
	baseSHA := string(tip)

	authorName, authorEmail, authoredBy := "runko-web", "runko-web@localhost", ""
	if principal != nil {
		authorName, authorEmail, authoredBy = principal.Name, principal.Name+"@runko", principal.Name
	}

	changeID := receive.GenerateChangeID("delete|" + plan.Path + "|" + baseSHA + "|" + time.Now().UTC().String())
	title := fmt.Sprintf("Delete project %s", name)
	msg := fmt.Sprintf("%s\n\nChange-Id: %s\n", title, changeID)

	overlay := core.Overlay{Changes: make([]core.FileChange, 0, len(plan.Ops))}
	paths := make([]string, 0, len(plan.Ops))
	for _, op := range plan.Ops {
		paths = append(paths, op.Path)
		if op.Action == "delete" {
			overlay.Changes = append(overlay.Changes, core.FileChange{Path: op.Path, Delete: true})
		} else {
			overlay.Changes = append(overlay.Changes, core.FileChange{Path: op.Path, Content: []byte(op.Content)})
		}
	}
	rev, err := g.CommitOverlay(core.Revision(baseSHA), overlay, core.CommitMeta{
		AuthorName: authorName, AuthorEmail: authorEmail, Message: msg,
	})
	if err != nil {
		return Change{}, internalErr(fmt.Errorf("apply delete plan: %w", err))
	}

	changeRef := "refs/changes/" + changeID + "/head"
	if err := g.UpdateRef(changeRef, rev, nil); err != nil {
		return Change{}, internalErr(fmt.Errorf("write change ref: %w", err))
	}
	change, err := s.Store.CreateOrUpdateChange(ctx, changeID, baseSHA, string(rev), changeRef, title, authoredBy, "", "")
	if err != nil {
		return Change{}, internalErr(fmt.Errorf("record change: %w", err))
	}
	if s.Processor != nil {
		s.Processor.computeAffectedAndEnqueue(ctx, change, paths, nil)
	}
	return change, nil
}

// listSubtree walks the tree at rev under root, returning every blob path.
func listSubtree(g *gitstore.Store, rev core.Revision, root string) ([]string, error) {
	var out []string
	var walk func(dir string) error
	walk = func(dir string) error {
		entries, err := g.GetTree(rev, dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			// ls-tree names entries relative to the queried directory.
			full := path.Join(dir, e.Path)
			if e.Type == "tree" {
				if err := walk(full); err != nil {
					return err
				}
				continue
			}
			out = append(out, full)
		}
		return nil
	}
	if err := walk(strings.TrimSuffix(root, "/")); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	principal := s.principalFor(r)
	change, apiErr := s.deleteProjectCore(r.Context(), r.PathValue("name"), principal)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"change_id": change.ChangeKey, "title": change.Title})
}
