package runkod

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/saxocellphone/runko/affected"
	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/core"
	"github.com/saxocellphone/runko/index"
	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/search"
)

// Server assembles every HTTP surface runkod exposes: smart-HTTP git
// hosting, the internal pre-receive callback, and the REST API (changes/
// checks/affected/merge-requirements). Deploy-token auth (§14.11's pattern,
// already used by `runko-ci report-check` in stage 9) is a single shared
// bearer token for this stage - not full OIDC (§15.1, doc.go's scope
// boundary).
type Server struct {
	RepoDir   string
	TrunkRef  string
	Store     Store
	Processor *Processor
	Token     string // deploy token (REST API) and pre-receive shared secret
	// Searcher backs GET /api/search (§8.3's search_code tool). Defaults to
	// search.NotConfiguredSearcher{} in Handler if left nil, so a daemon
	// started without --search-url still answers with a structured "not
	// configured" error rather than panicking.
	Searcher search.CodeSearcher
}

// Handler assembles the full mux: smart-HTTP git hosting at
// /<RepoMountName>/, the internal pre-receive callback, and the bearer-
// token-authed REST API.
func (s *Server) Handler() (http.Handler, error) {
	mux := http.NewServeMux()

	gitHandler, err := GitHTTPHandler(s.RepoDir)
	if err != nil {
		return nil, fmt.Errorf("runkod: build git smart-HTTP handler: %w", err)
	}
	mux.Handle("/"+RepoMountName(s.RepoDir)+"/", s.requireGitAuth(gitHandler))

	mux.HandleFunc("POST /internal/pre-receive", s.handlePreReceive)

	mux.HandleFunc("GET /api/changes/{key}", s.requireAuth(s.handleGetChange))
	mux.HandleFunc("GET /api/changes/{key}/affected", s.requireAuth(s.handleGetAffected))
	mux.HandleFunc("GET /api/changes/{key}/merge-requirements", s.requireAuth(s.handleGetMergeRequirements))
	mux.HandleFunc("POST /api/changes/{key}/checks", s.requireAuth(s.handlePostCheck))
	mux.HandleFunc("GET /api/search", s.requireAuth(s.handleSearch))

	return mux, nil
}

// searcher returns s.Searcher, or search.NotConfiguredSearcher{} if unset -
// so callers (Handler's route, tests constructing a bare Server{}) never
// need to nil-check before calling Search.
func (s *Server) searcher() search.CodeSearcher {
	if s.Searcher == nil {
		return search.NotConfiguredSearcher{}
	}
	return s.Searcher
}

// requireAuth wraps a handler with deploy-token bearer auth. The
// /internal/pre-receive callback checks the SAME token itself (it isn't
// wrapped here, since it uses the shared secret as authentication between
// the daemon and its own installed hook, not a client-facing API).
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenMatches(r.Header.Get("Authorization")) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) tokenMatches(authHeader string) bool {
	want := "Bearer " + s.Token
	return subtle.ConstantTimeCompare([]byte(authHeader), []byte(want)) == 1
}

// requireGitAuth gates the smart-HTTP git transport itself with the same
// deploy token (§14.11, doc.go's scope boundary) - without this, anyone with
// network access could clone/push regardless of what the pre-receive hook
// enforces, since hooks only govern policy, not who may connect at all. Git
// clients authenticate via plain HTTP Basic (`git clone
// http://<any-user>:<token>@host/<repo>.git`), which every git client
// supports natively - no custom credential helper needed.
func (s *Server) requireGitAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(s.Token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="runko"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Headers the hidden `runkod hook pre-receive` subcommand forwards from its
// own process environment - git's object quarantine sets
// GIT_OBJECT_DIRECTORY/GIT_ALTERNATE_OBJECT_DIRECTORIES on the hook's
// process only, and the daemon (a separate process the hook calls over
// HTTP) needs them explicitly to see a push's not-yet-committed objects
// (see internal/gitstore.Store.ExtraEnv, runkod/prereceive.go).
const (
	headerGitObjectDirectory            = "X-Git-Object-Directory"
	headerGitAlternateObjectDirectories = "X-Git-Alternate-Object-Directories"
)

// handlePreReceive is the internal callback the installed pre-receive hook
// (hook.go) calls back to - the actual write-path enforcement (§7.4, §11.5)
// lives in Processor, running here inside the daemon process, since the
// hook itself is a grandchild process with no access to the daemon's Store.
func (s *Server) handlePreReceive(w http.ResponseWriter, r *http.Request) {
	if !s.tokenMatches(r.Header.Get("Authorization")) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	updates, err := ParseRefUpdates(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var extraEnv []string
	if v := r.Header.Get(headerGitObjectDirectory); v != "" {
		extraEnv = append(extraEnv, "GIT_OBJECT_DIRECTORY="+v)
	}
	if v := r.Header.Get(headerGitAlternateObjectDirectories); v != "" {
		extraEnv = append(extraEnv, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+v)
	}

	results := s.Processor.ProcessBatch(r.Context(), updates, extraEnv)
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleGetChange(w http.ResponseWriter, r *http.Request) {
	change, ok, err := s.Store.GetChange(r.Context(), r.PathValue("key"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, change)
}

// handleGetAffected computes the platform-floor affected.Result live from
// the repo (§13.3: affected is a pure function of tree state, never cached
// past a head_sha change) - the same computation runko-ci's own `affected`
// command performs, just against the Change's stored base/head instead of
// CLI flags.
func (s *Server) handleGetAffected(w http.ResponseWriter, r *http.Request) {
	change, ok, err := s.Store.GetChange(r.Context(), r.PathValue("key"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}

	store := gitstore.New(s.RepoDir)
	indexed, err := index.Scan(store, core.Revision(change.HeadSHA), nil)
	if err != nil {
		http.Error(w, fmt.Sprintf("scan projects: %v", err), http.StatusInternalServerError)
		return
	}
	projects := make([]affected.ProjectInfo, len(indexed))
	for i, p := range indexed {
		projects[i] = affected.ProjectInfo{Name: p.Name, Path: p.Path, DeclaredDependencies: p.DeclaredDependencies}
	}

	base := change.BaseSHA
	if base == "" {
		base = emptyTreeOID
	}
	changedPaths, err := gitDiffNamesOnly(s.RepoDir, base, change.HeadSHA)
	if err != nil {
		http.Error(w, fmt.Sprintf("diff: %v", err), http.StatusInternalServerError)
		return
	}

	result := affected.Compute(projects, changedPaths, affected.Options{})
	writeJSON(w, http.StatusOK, result)
}

// handleGetMergeRequirements assembles MergeRequirements from whatever
// check runs have been reported for the Change's current head_sha. Owners
// requirements, individually-required check names, check-set policies, and
// require_build_binding are all empty here - this daemon doesn't yet
// resolve org policy or tree-based owners (that plumbing is index/receive's
// job, not wired into this REST layer this session) - so this reports a
// minimal-but-correct MergeRequirements (mergeable unless a reported check
// is failing/pending), not a placeholder shape.
func (s *Server) handleGetMergeRequirements(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	change, ok, err := s.Store.GetChange(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}
	runs, err := s.Store.ListCheckRuns(r.Context(), key, change.HeadSHA)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	requiredNames := make([]string, len(runs))
	for i, run := range runs {
		requiredNames[i] = run.Name
	}
	req := checks.ComputeMergeRequirements(key, nil, requiredNames, runs, nil, nil, nil)
	writeJSON(w, http.StatusOK, req)
}

// checkRunReport mirrors cmd/runko-ci's CheckRunReport exactly (the POST
// /changes/{id}/checks body it already sends) - this is the endpoint
// `runko-ci report-check` round-trips against.
type checkRunReport struct {
	Name       string `json:"name"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion,omitempty"`
	DetailsURL string `json:"details_url,omitempty"`
	Reporter   string `json:"reporter"`
}

func (s *Server) handlePostCheck(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	change, ok, err := s.Store.GetChange(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}

	var report checkRunReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, fmt.Sprintf("decode check run: %v", err), http.StatusBadRequest)
		return
	}
	if report.Name == "" || report.ExternalID == "" || report.Reporter == "" {
		http.Error(w, "name, external_id, and reporter are required", http.StatusBadRequest)
		return
	}

	run := checks.CheckRunView{
		Name:       report.Name,
		Status:     checks.CheckStatus(report.Status),
		Conclusion: checks.CheckConclusion(report.Conclusion),
	}
	if err := s.Store.UpsertCheckRun(r.Context(), key, change.HeadSHA, run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// handleSearch implements search_code (§8.3): a project-tagged code search
// over trunk, served through the daemon (stage 11's DAG entry). Project
// tagging happens here, not inside search.CodeSearcher - that package stays
// a leaf (no dependency on index/affected), the same layering
// handleGetAffected already uses (its own index.Scan, then affected.Compute).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		http.Error(w, "q is required", http.StatusBadRequest)
		return
	}
	num := 0
	if n := r.URL.Query().Get("num"); n != "" {
		if v, err := strconv.Atoi(n); err == nil {
			num = v
		}
	}

	result, err := s.searcher().Search(r.Context(), q, search.SearchOptions{Num: num})
	if err != nil {
		var ce *clierr.Error
		if errors.As(err, &ce) {
			writeJSON(w, http.StatusServiceUnavailable, ce)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	gstore := gitstore.New(s.RepoDir)
	if indexed, ierr := index.Scan(gstore, core.Revision("refs/heads/"+s.TrunkRef), nil); ierr == nil {
		tagProjects(result, indexed)
	}

	writeJSON(w, http.StatusOK, result)
}

// tagProjects fills each Hit's Project by the same longest-path-prefix rule
// affected.Compute uses (§13.3) - duplicated here in miniature rather than
// exported from affected/, since that package's findOwner is deliberately
// unexported and this is a three-line rule, not worth a cross-package API
// for.
func tagProjects(result *search.Result, projects []index.IndexedProject) {
	for i, hit := range result.Hits {
		var best index.IndexedProject
		found := false
		for _, p := range projects {
			matches := p.Path == "" || hit.Path == p.Path || strings.HasPrefix(hit.Path, p.Path+"/")
			if !matches {
				continue
			}
			if !found || len(p.Path) > len(best.Path) {
				best = p
				found = true
			}
		}
		if found {
			result.Hits[i].Project = best.Name
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// gitDiffNamesOnly returns the paths that differ between two revisions -
// the REST layer's own copy of the same `git diff --name-only` primitive
// cmd/runko-ci shells out to, since this package has no dependency on that
// command package.
func gitDiffNamesOnly(repoDir, from, to string) ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", from, to)
	cmd.Dir = repoDir
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff --name-only %s %s: %w: %s", from, to, err, strings.TrimSpace(errBuf.String()))
	}
	trimmed := strings.TrimSpace(out.String())
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}
