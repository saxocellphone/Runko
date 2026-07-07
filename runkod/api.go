package runkod

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

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
	mux.HandleFunc("POST /api/changes/{key}/land", s.requireAuth(s.handleLandChange))
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
	req, err := s.mergeRequirements(r.Context(), key, change)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// mergeRequirements is handleGetMergeRequirements' computation, factored out
// so handleLandChange can gate on the exact same Mergeable bool a client
// would have seen from GET .../merge-requirements - one source of truth for
// "is this Change allowed to land", not two computations that could drift.
func (s *Server) mergeRequirements(ctx context.Context, key string, change Change) (checks.MergeRequirements, error) {
	runs, err := s.Store.ListCheckRuns(ctx, key, change.HeadSHA)
	if err != nil {
		return checks.MergeRequirements{}, err
	}
	requiredNames := make([]string, len(runs))
	for i, run := range runs {
		requiredNames[i] = run.Name
	}
	return checks.ComputeMergeRequirements(key, nil, requiredNames, runs, nil, nil, nil), nil
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

// handleLandChange implements POST .../land (§13.5, §28.3 stage 11b): the
// write-path verb the daemon was missing entirely until this stage - land.
// Land (stage 7) and the merge-requirements gate (stage 8) both existed and
// were both fully tested, but nothing wired them together into the daemon,
// so stage 14's create->change->land loop had no wire-level "land" to call.
// Gated on the exact same Mergeable bool GET .../merge-requirements reports
// (mergeRequirements above) - never a silent land of a Change with failing
// or pending checks.
func (s *Server) handleLandChange(w http.ResponseWriter, r *http.Request) {
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
	if change.State == "landed" {
		// Idempotent: a client retrying a land request after a dropped
		// response (or simply asking again) should see the same success,
		// not a confusing "not mergeable"/re-attempt error.
		writeJSON(w, http.StatusOK, landResponse{Landed: true, LandedSHA: change.LandedSHA})
		return
	}

	mr, err := s.mergeRequirements(r.Context(), key, change)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !mr.Mergeable {
		writeJSON(w, http.StatusConflict, &clierr.Error{
			Code:       "not_mergeable",
			Field:      "change",
			Message:    fmt.Sprintf("change %s is not mergeable yet", key),
			Suggestion: strings.Join(mr.Blockers, "; "),
			DocURL:     "docs/design.md#136-merge-gates-and-landing",
		})
		return
	}

	outcome, err := s.attemptLand(r.Context(), change)
	if err != nil {
		http.Error(w, fmt.Sprintf("land: %v", err), http.StatusInternalServerError)
		return
	}

	switch {
	case outcome.Landed:
		if _, err := s.Store.MarkChangeLanded(r.Context(), key, outcome.LandedSHA); err != nil {
			http.Error(w, fmt.Sprintf("land: record landed state: %v", err), http.StatusInternalServerError)
			return
		}
		s.enqueueLandedWebhook(r.Context(), change, outcome.LandedSHA)
		if s.Processor != nil {
			s.Processor.ZoektIndexWorker.Trigger()
		}
		writeJSON(w, http.StatusOK, landResponse{Landed: true, LandedSHA: outcome.LandedSHA})
	case outcome.RequiresRevalidation:
		writeJSON(w, http.StatusConflict, &clierr.Error{
			Code:       "requires_revalidation",
			Field:      "change",
			Message:    "trunk has moved in a way that intersects this change's affected set",
			Suggestion: "re-run required checks against current trunk, then retry land",
			DocURL:     "docs/design.md#135-optimistic-revalidation",
		})
	case len(outcome.Conflicts) > 0:
		writeJSON(w, http.StatusConflict, &clierr.Error{
			Code:       "merge_conflict",
			Field:      "change",
			Message:    fmt.Sprintf("rebase produced conflicts in: %s", strings.Join(outcome.Conflicts, ", ")),
			Suggestion: "rebase locally, resolve conflicts, and push an updated Change",
			DocURL:     "docs/design.md#134-rebase-based-landing",
		})
	default: // exhausted maxLandRaceRetries
		writeJSON(w, http.StatusConflict, &clierr.Error{
			Code:       "race_retry_exhausted",
			Field:      "change",
			Message:    "trunk kept moving faster than this land attempt could keep up",
			Suggestion: "retry the land request",
			DocURL:     "docs/design.md#135-optimistic-revalidation",
		})
	}
}

// landResponse is the successful-land wire shape - deliberately smaller
// than land.Outcome (which also carries RequiresRevalidation/Conflicts/
// RaceRetry, all represented as this endpoint's non-200 clierr.Error
// responses instead, per the rest of this API's convention of one
// structured shape per outcome rather than one big oneOf-like struct).
type landResponse struct {
	Landed    bool
	LandedSHA string
}

// enqueueLandedWebhook mirrors Processor.computeAffectedAndEnqueue's
// change.updated envelope construction (prereceive.go) for change.landed -
// already a valid docs/spec/webhooks/webhook-envelope.schema.json "type"
// enum value with no extra required fields, so no schema change was needed
// for this stage. Errors are logged, not fatal to the request: the Change
// is already durably marked landed, so a failed webhook enqueue shouldn't
// turn a successful land response into an error - same reasoning as
// computeAffectedAndEnqueue's own doc comment.
func (s *Server) enqueueLandedWebhook(ctx context.Context, change Change, landedSHA string) {
	env := checks.WebhookEnvelope{
		SpecVersion: "1",
		DeliveryID:  change.ChangeKey + "@landed@" + landedSHA,
		Type:        "change.landed",
		OccurredAt:  time.Now(),
		Change: checks.WebhookChange{
			ID: change.ChangeKey, State: "landed",
			BaseSHA: change.BaseSHA, HeadSHA: landedSHA, GitRef: change.GitRef,
			Title: change.Title,
			Actor: checks.WebhookActor{Type: "user", ID: "unknown"},
		},
	}
	payload, err := checks.MarshalEnvelope(env)
	if err != nil {
		log.Printf("runkod: %s: marshal change.landed webhook: %v", change.ChangeKey, err)
		return
	}
	if _, err := s.Store.EnqueueWebhook(ctx, env.Type, payload); err != nil {
		log.Printf("runkod: %s: enqueue change.landed webhook: %v", change.ChangeKey, err)
	}
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
