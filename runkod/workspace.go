// Workspace registry (§12.2's data model, §12.4's sidecar REST endpoints,
// §28.3 stage 12b). The registry row is metadata ONLY - durable content
// lives in Git as snapshot commits on refs/workspaces/<id>/head, pushed
// through the same receive funnel as Changes (§11.5); Postgres never holds
// file content (§12.2's invariant).
package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/saxocellphone/runko/core"
	"github.com/saxocellphone/runko/index"
	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
)

// Workspace is one registry row - §12.2's model thinned to what Phase A
// (git-glue workspaces, §12.3) needs. ID is the human handle ("payments-fix")
// and doubles as the ref segment: refs/workspaces/<ID>/head. Owner is
// client-supplied text until real AuthN exists (§15.1), the same v1 trust
// boundary as report-check's reporter and approve's approved_by.
type Workspace struct {
	ID              string
	Owner           string
	BaseRevision    string
	ProjectAffinity []string // project names (§12.2 project_affinity)
	WriteAllowlist  []string // path roots computed from affinity (§12.2)
	SnapshotRef     string   // refs/workspaces/<ID>/head
	Status          string   // "active" | "detached" | "closed"
}

// workspaceIDPattern keeps IDs safe as a git ref segment (and as a URL path
// segment) - the full git-check-ref-format rules are stricter than needed;
// this conservative subset avoids every sharp edge (no "..", no "@{", no
// leading/trailing dots, no slashes).
var workspaceIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

// SnapshotRefWorkspaceID extracts the workspace id from a
// refs/workspaces/<id>/... ref name, reporting ok=false for anything else.
func SnapshotRefWorkspaceID(ref string) (string, bool) {
	rest, found := strings.CutPrefix(ref, "refs/workspaces/")
	if !found {
		return "", false
	}
	id, _, found := strings.Cut(rest, "/")
	if !found || id == "" {
		return "", false
	}
	return id, true
}

// createWorkspaceRequest is POST /api/workspaces' body.
type createWorkspaceRequest struct {
	Name     string   `json:"name"`
	Owner    string   `json:"owner"`
	Projects []string `json:"projects"`
}

// workspaceResponse enriches a registry row with what a client needs to
// build the local checkout: cone patterns for `git sparse-checkout set` and
// the repo mount path for composing the git remote URL (the daemon serves
// smart-HTTP at /<RepoPath>/, api.go's GitHTTPHandler).
type workspaceResponse struct {
	Workspace
	SparsePatterns []string
	RepoPath       string
	TrunkRef       string // what `workspace update-base` fetches
}

func (s *Server) workspaceResponse(ws Workspace) workspaceResponse {
	return workspaceResponse{
		Workspace:      ws,
		SparsePatterns: ws.WriteAllowlist,
		RepoPath:       RepoMountName(s.RepoDir),
		TrunkRef:       s.TrunkRef,
	}
}

// resolveProjectPaths maps project names to their tree paths at rev,
// erroring with the unknown names - a client typo must name the culprit
// (§6.5), not silently create a workspace with a hole in its cone.
func (s *Server) resolveProjectPaths(rev core.Revision, names []string) (paths []string, unknown []string, err error) {
	store := gitstore.New(s.RepoDir)
	indexed, err := index.Scan(store, rev, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("scan projects: %w", err)
	}
	byName := make(map[string]index.IndexedProject, len(indexed))
	for _, p := range indexed {
		byName[p.Name] = p
	}
	for _, n := range names {
		p, ok := byName[n]
		if !ok {
			unknown = append(unknown, n)
			continue
		}
		paths = append(paths, p.Path)
	}
	sort.Strings(paths)
	return paths, unknown, nil
}

// createWorkspaceCore is POST /api/workspaces' decision core, shared with
// the Connect CreateWorkspace RPC (rpc.go) - see actions.go on the pattern.
func (s *Server) createWorkspaceCore(ctx context.Context, name, owner string, projects []string) (Workspace, *apiError) {
	if !workspaceIDPattern.MatchString(name) {
		return Workspace{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_workspace_name", Field: "name",
			Message:    fmt.Sprintf("%q is not a valid workspace name", name),
			Suggestion: "use letters, digits, dots, dashes, underscores; start with a letter or digit",
		})
	}
	if owner == "" || len(projects) == 0 {
		return Workspace{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "projects",
			Message:    "owner and at least one project are required",
			Suggestion: "runko workspace create --name <n> --by <you> --project <p> --runkod-url <url> --token <t>",
		})
	}

	gstore := gitstore.New(s.RepoDir)
	base, err := gstore.ResolveRef("refs/heads/" + s.TrunkRef)
	if err != nil {
		return Workspace{}, typedErr(http.StatusConflict, clierr.Error{
			Code: "trunk_unborn", Field: "monorepo",
			Message:    fmt.Sprintf("trunk %s has no commits yet - a workspace needs a base revision", s.TrunkRef),
			Suggestion: "land the monorepo's first change, then create the workspace",
		})
	}

	paths, unknown, err := s.resolveProjectPaths(base, projects)
	if err != nil {
		return Workspace{}, &apiError{Status: http.StatusInternalServerError, Err: clierr.Error{Message: err.Error()}}
	}
	if len(unknown) > 0 {
		return Workspace{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "unknown_project", Field: "projects",
			Message:    fmt.Sprintf("no such project(s): %s", strings.Join(unknown, ", ")),
			Suggestion: "runko project list --runkod-url <url> --token <t>  # see the names indexed at trunk",
		})
	}

	ws := Workspace{
		ID: name, Owner: owner,
		BaseRevision:    string(base),
		ProjectAffinity: append([]string{}, projects...),
		WriteAllowlist:  paths,
		SnapshotRef:     "refs/workspaces/" + name + "/head",
		Status:          "active",
	}
	created, err := s.Store.CreateWorkspace(ctx, ws)
	if err != nil {
		return Workspace{}, typedErr(http.StatusConflict, clierr.Error{
			Code: "workspace_exists", Field: "name",
			Message:    fmt.Sprintf("workspace %q already exists", name),
			Suggestion: "pick another name, or `runko workspace attach " + name + "` to resume it",
		})
	}
	return created, nil
}

func (s *Server) handleCreateWorkspace(w http.ResponseWriter, r *http.Request) {
	var req createWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_body", Message: "request body must be JSON with name, owner, and projects",
		})
		return
	}
	created, apiErr := s.createWorkspaceCore(r.Context(), req.Name, req.Owner, req.Projects)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusCreated, s.workspaceResponse(created))
}

func (s *Server) handleListWorkspaces(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.ListWorkspaces(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, ok, err := s.Store.GetWorkspace(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, s.workspaceResponse(ws))
}

// updateWorkspaceBaseCore records a client-side update-base (fetch +
// rebase, §12.3's "sync base" row) in the registry. The revision must exist
// in the repo - the registry never points at a base the server can't see.
func (s *Server) updateWorkspaceBaseCore(ctx context.Context, id, baseRevision string) (Workspace, *apiError) {
	if baseRevision == "" {
		return Workspace{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "base_revision",
			Message: "base_revision is required",
		})
	}
	if _, err := gitstore.New(s.RepoDir).ResolveRef(baseRevision + "^{commit}"); err != nil {
		return Workspace{}, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "unknown_revision", Field: "base_revision",
			Message:    fmt.Sprintf("%q is not a commit this monorepo knows", baseRevision),
			Suggestion: "push or land first, then update the base",
		})
	}
	ws, err := s.Store.UpdateWorkspaceBase(ctx, id, baseRevision)
	if err != nil {
		return Workspace{}, plainErr(http.StatusNotFound, "workspace not found")
	}
	return ws, nil
}

func (s *Server) handleUpdateWorkspaceBase(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseRevision string `json:"base_revision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "base_revision",
			Message: "base_revision is required",
		})
		return
	}
	ws, apiErr := s.updateWorkspaceBaseCore(r.Context(), r.PathValue("id"), req.BaseRevision)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

// handleSparsePatterns is §12.4's `GET /sparse-patterns?projects=…` - cone
// patterns from the project graph, for clients configuring a checkout
// without creating a workspace.
func (s *Server) handleSparsePatterns(w http.ResponseWriter, r *http.Request) {
	names := splitCommaList(r.URL.Query().Get("projects"))
	if len(names) == 0 {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "projects",
			Message: "pass ?projects=<name>[,<name>...]",
		})
		return
	}
	gstore := gitstore.New(s.RepoDir)
	base, err := gstore.ResolveRef("refs/heads/" + s.TrunkRef)
	if err != nil {
		writeJSON(w, http.StatusConflict, clierr.Error{
			Code: "trunk_unborn", Field: "monorepo",
			Message: fmt.Sprintf("trunk %s has no commits yet", s.TrunkRef),
		})
		return
	}
	paths, unknown, err := s.resolveProjectPaths(base, names)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(unknown) > 0 {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "unknown_project", Field: "projects",
			Message:    fmt.Sprintf("no such project(s): %s", strings.Join(unknown, ", ")),
			Suggestion: "runko project list --runkod-url <url> --token <t>  # see the names indexed at trunk",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{"patterns": paths})
}

func splitCommaList(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
