// Multi-org routing (§7.1's Org -> Monorepo model reaching the daemon,
// 2026-07-08). The schema has been multi-tenant since stage 2 (everything
// keys on org_id/monorepo_id); this closes the daemon-side gap: each org
// owns its own bare repo and gets its own Server instance - the whole
// existing surface (smart-HTTP git, REST, Connect RPC, the pre-receive
// callback) mounted unchanged under /o/<org>/. A base URL is the only
// thing a client needs, so `runko --runkod-url https://host/o/acme` and
// the web transport work against an org with zero client changes.
//
// The root-mounted repo the daemon has always served stays as the
// "default org" (also reachable at /o/<default-name>/ for uniformity).
// Since 2026-07-09 sessions are ORG-SCOPED: every org - the default one
// included - is membership-gated for store-backed accounts, GET /api/orgs
// lists exactly the caller's memberships, and logging in means logging
// into an org. Operator principals and the deploy token stay server-wide
// (they run the place); accounts remain server-global rows - identity is
// global, REACH is per-org.
//
// Deliberately NOT here in v1: org deletion (a repo is not something a
// REST DELETE should vaporize), per-org zoekt/mirror config (daemon
// singletons still apply to the default org only), org-scoped operator
// principals (operator config stays server-wide).
package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/agentsmd"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/land"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/saxocellphone/runko/internal/clierr"
)

// Directory is the server-global account + membership view backing every
// org's auth (auth.go) and the hub's org APIs. *PostgresStore (shared
// pool) and *MemStore both implement it; the hub points every org-scoped
// Server at ONE directory so "who exists" and "who belongs where" can
// never differ between orgs.
type Directory interface {
	// Accounts are PER-ORG (migration 0017): (org, name) is the identity;
	// the same name in two orgs is two independent accounts.
	GetStoredPrincipal(ctx context.Context, org, name string) (StoredPrincipal, bool, error)
	// ListPrincipalOrgs returns every org-scoped account carrying a name -
	// cross-org resolution (auth.go) and the hub org selector ride on it.
	ListPrincipalOrgs(ctx context.Context, name string) ([]StoredPrincipal, error)
	// EnsureOrg registers the org row (idempotent). Repo/server assembly
	// is the hub's job, not the directory's.
	EnsureOrg(ctx context.Context, name string) error
	OrgMemberRole(ctx context.Context, orgName, principal string) (role string, member bool, err error)
	UpsertOrgMember(ctx context.Context, orgName, principal, role string) error
	RemoveOrgMember(ctx context.Context, orgName, principal string) error
	ListOrgMembers(ctx context.Context, orgName string) ([]OrgMember, error)
	ListOrgMemberships(ctx context.Context, principal string) ([]OrgMembership, error)
	ListOrgNames(ctx context.Context) ([]string, error)
	// ListOrgRecords is the admin view: every org row, archived included.
	ListOrgRecords(ctx context.Context) ([]OrgRecord, error)
	// SetOrgArchived flips the archive bit (admin panel; finding #19's
	// org lifecycle). Archived orgs keep their row and repo - recovery
	// is unarchive, never restore-from-backup.
	SetOrgArchived(ctx context.Context, orgName string, archived bool) error
	// GetOrgSettings returns the zero value for an org with nothing set.
	GetOrgSettings(ctx context.Context, orgName string) (OrgSettings, error)
	UpdateOrgSettings(ctx context.Context, orgName string, settings OrgSettings) error
}

// OrgMember is one account's membership in one org.
type OrgMember struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// OrgRecord is one org row as the ADMIN panel sees it.
type OrgRecord struct {
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

// OrgSettings is the org settings page's storage shape (migration 0008,
// JSONB on the org row). Deliberately thin - real merge policy lives in
// the tree (§9.4); these are the org-level knobs that were daemon flags
// before multi-org made the org a first-class row.
type OrgSettings struct {
	Description string `json:"description,omitempty"`
	// GithubMirrorRepo is the org's GitHub mirror in "owner/name" form,
	// owned by POST /api/github/connect (2026-07-16, runkod/README.md):
	// the daemon-level GitHub App mints its push credentials, and the
	// wiring survives restarts through this field. The settings PUT
	// carries it forward untouched - the settings page never edits it.
	GithubMirrorRepo string `json:"github_mirror_repo,omitempty"`
	// GlobalRequiredChecks are required on EVERY change in this org
	// (§14.9), merged with the daemon-level --global-required-checks.
	GlobalRequiredChecks []string `json:"global_required_checks,omitempty"`
	// RevalidationPolicy is the org's §13.5 revalidation tier
	// (conflict-only | affected-intersection | always; "" defers to the
	// daemon flag, then the conflict-only default - 2026-07-15). "never"
	// is refused at write time: it is the admin force override, not a
	// policy.
	RevalidationPolicy string `json:"revalidation_policy,omitempty"`
	// PublicRead opts this org in to anonymous READ access (§15.2, decided
	// 2026-07-09): git upload-pack, the REST GET allowlist, and the read
	// RPCs - never writes, workspaces, settings, or members. Enabling it
	// is refused while any trunk manifest declares visibility: restricted
	// (restricted-read must hold at every surface or not at all, and
	// anonymous fetch has no per-principal filtering until §12.3 Phase B).
	PublicRead bool `json:"public_read,omitempty"`
	// RequireResolvedThreads makes unresolved review threads a §13.5 merge
	// blocker (§13.4.1, decided 2026-07-10). Default off - the ceremony
	// budget (§2.3); GitHub defaults the same knob off for the same reason.
	RequireResolvedThreads bool `json:"require_resolved_threads,omitempty"`
	// EnforceTagPolicy gates refs/tags/* writes at receive (§14.10.3,
	// stage 17): org admins/releasers, tag-scoped bot lanes, and the
	// operator credential only. Default off = the documented v1
	// permissiveness; flipping it is the org's move to the default-deny
	// posture for its release surface.
	EnforceTagPolicy bool `json:"enforce_tag_policy,omitempty"`
}

// OrgMembership is one (org, role) pair for a principal. Roles: "admin"
// (may add members) or "member".
type OrgMembership struct {
	Org  string `json:"org"`
	Role string `json:"role"`
}

// orgNamePattern is deliberately tighter than principal names: the org
// name is a URL path segment, a directory name, and a git remote
// component all at once.
var orgNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,38}$`)

// reservedOrgNames can never be org names - they collide with sibling
// URL surfaces (or would just confuse: "repo" is every org's repo mount).
var reservedOrgNames = map[string]bool{
	"api": true, "o": true, "internal": true, "demo": true,
	"landing": true, "assets": true, "repo": true,
	"healthz": true, "readyz": true, "metrics": true,
	// The web SPA's root routes: orgs get GitHub-style path URLs
	// (/<org>/browse, ...), so an org named after an app route would
	// shadow it in every browser.
	"changes": true, "browse": true, "projects": true,
	"workspaces": true, "search": true, "settings": true,
	"admin": true, "graph": true, "login": true, "signup": true,
}

// OrgHub owns the org registry and the /o/<name>/ router. Construction
// of per-org Stores and Servers is injected (NewOrgStore/NewOrgServer):
// the daemon entrypoint holds all the config (scanner, principals,
// flags) a Server needs, and tests inject lightweight variants.
type OrgHub struct {
	// Default is the root-mounted Server (the historical single-org
	// layout). Its credential resolver also authenticates the hub's own
	// org APIs, and its Store doubles as Directory when Directory is nil.
	Default *Server
	// DefaultOrgName names the default org in listings and mounts it at
	// /o/<name>/ alongside its root mount.
	DefaultOrgName string
	// DataDir holds per-org repos at DataDir/<org>/repo.git.
	DataDir string
	// SelfURL is the daemon's own base URL for hook callbacks - an org
	// repo's pre-receive hook calls back to SelfURL/o/<org>/internal/pre-receive.
	SelfURL string
	// AllowOrgCreate gates POST /api/orgs (default-deny posture: an
	// operator must opt a deployment into self-service org creation).
	AllowOrgCreate bool
	Directory      Directory
	// NewOrgStore builds an org's Store (MemStore, or a PostgresStore on
	// the shared pool - which also creates the org row durably).
	NewOrgStore func(ctx context.Context, orgName string) (Store, error)
	// NewOrgServer assembles an org's Server + Processor around its repo
	// and store, mirroring the daemon entrypoint's default-org wiring.
	// The hub stamps OrgName/Directory afterwards - membership gating is
	// its concern, not the constructor's.
	NewOrgServer func(orgName, repoDir string, store Store) (*Server, error)
	// StartOrgWorkers (optional) starts per-org background work (webhook
	// outbox). ctx is the daemon's lifetime.
	StartOrgWorkers func(ctx context.Context, orgName string, store Store)
	// Ctx bounds per-org workers; nil means context.Background().
	Ctx context.Context

	mu       sync.Mutex
	orgs     map[string]http.Handler
	archived map[string]bool
}

func (h *OrgHub) ctx() context.Context {
	if h.Ctx != nil {
		return h.Ctx
	}
	return context.Background()
}

func (h *OrgHub) repoDirFor(orgName string) string {
	return filepath.Join(h.DataDir, orgName, "repo.git")
}

// Handler wraps the default server's full handler with the org router
// and the hub's own org APIs.
//
// ORG-LESS MODE (2026-07-17, the default-org retirement): when
// DefaultOrgName is "", there is no root-mounted org at all - h.Default
// is an AUTH-ONLY Server (accounts, signup config, credential
// resolution; no repo, no Processor) whose Handler() is never built.
// The hub then serves the global surfaces plus its own ops floor
// (healthz/readyz/metrics) at the root, and every org - the first one
// included - lives at /o/<name>/. The historical mode (a repo-dir'd
// default org served at the root) is unchanged when DefaultOrgName is
// set.
func (h *OrgHub) Handler() (http.Handler, error) {
	var defaultHandler http.Handler
	if h.DefaultOrgName != "" {
		dh, err := h.Default.Handler()
		if err != nil {
			return nil, err
		}
		defaultHandler = dh
	}
	mux := http.NewServeMux()
	// rpcMiddleware (not requireAuth) so the browser's CORS preflight
	// works from a dev-server origin, same as every other web-facing
	// route. It authenticates against the DEFAULT server: accounts are
	// global, and org membership doesn't gate knowing which orgs you're
	// in or asking for a new one.
	// Registered WITHOUT method qualifiers: the browser's CORS preflight
	// is an OPTIONS request that must reach rpcMiddleware (which answers
	// it) instead of falling through to the default handler's 404 -
	// found by driving the real dev loop (Vite origin != daemon origin).
	// rpcMiddlewareGlobal, not rpcMiddleware: these are GLOBAL-account
	// routes ("which orgs am I in", "create one", per-org admin surfaces
	// that gate themselves via orgAccess) - the default server's own
	// membership gate must not swallow callers who belong elsewhere.
	orgsAuthed := h.Default.rpcMiddlewareGlobal(byMethod(map[string]http.HandlerFunc{
		http.MethodGet: h.handleListOrgs, http.MethodPost: h.handleCreateOrg,
	}))
	mux.Handle("/api/orgs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Credential-less GET reaches the handler's anonymous branch
		// (public-org discovery, §15.2); everything else - POST, OPTIONS
		// preflight, any presented credential - takes the authed path.
		if r.Method == http.MethodGet && r.Header.Get("Authorization") == "" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			h.handleListOrgs(w, r)
			return
		}
		orgsAuthed.ServeHTTP(w, r)
	}))
	mux.Handle("/api/orgs/{org}/members", h.Default.rpcMiddlewareGlobal(byMethod(map[string]http.HandlerFunc{
		http.MethodGet: h.handleListOrgMembers, http.MethodPost: h.handleAddOrgMember,
	})))
	mux.Handle("/api/orgs/{org}/members/{name}", h.Default.rpcMiddlewareGlobal(byMethod(map[string]http.HandlerFunc{
		http.MethodDelete: h.handleRemoveOrgMember,
	})))
	mux.Handle("/api/orgs/{org}/settings", h.Default.rpcMiddlewareGlobal(byMethod(map[string]http.HandlerFunc{
		http.MethodGet: h.handleGetOrgSettings, http.MethodPut: h.handlePutOrgSettings,
	})))
	// Deployment admin surface (operator-only), ALL of it under
	// /api/admin/: its own sign-in check (whoami), the org estate
	// archived included, operator org creation, and the archive
	// lifecycle (finding #19). The admin panel (webadmin/) is a separate
	// app with its own sign-in flow - it talks to exactly this prefix
	// and never rides the org-scoped surface, so nothing here keys on
	// org membership or the normal login's stored session.
	mux.Handle("/api/admin/whoami", h.Default.rpcMiddlewareGlobal(byMethod(map[string]http.HandlerFunc{
		http.MethodGet: h.handleAdminWhoami,
	})))
	mux.Handle("/api/admin/orgs", h.Default.rpcMiddlewareGlobal(byMethod(map[string]http.HandlerFunc{
		http.MethodGet: h.handleAdminOrgs, http.MethodPost: h.handleAdminCreateOrg,
	})))
	mux.Handle("/api/admin/orgs/{org}/archive", h.Default.rpcMiddlewareGlobal(byMethod(map[string]http.HandlerFunc{
		http.MethodPost: h.archiveHandler(true),
	})))
	mux.Handle("/api/admin/orgs/{org}/unarchive", h.Default.rpcMiddlewareGlobal(byMethod(map[string]http.HandlerFunc{
		http.MethodPost: h.archiveHandler(false),
	})))
	// Sign-up at the hub level supersedes the default server's own
	// handler (more-specific mux pattern): the standard SaaS shape is
	// account + org in ONE step, and org assembly is the hub's job. The
	// default server's registration still serves pre-hub embedders.
	mux.HandleFunc("/api/signup", publicCORS(http.MethodPost, h.handleSignup))
	mux.HandleFunc("/api/auth/config", publicCORS(http.MethodGet, h.handleAuthConfig))
	mux.HandleFunc("/o/{org}/", func(w http.ResponseWriter, r *http.Request) {
		h.routeOrg(w, r, defaultHandler)
	})
	if defaultHandler != nil {
		mux.Handle("/", defaultHandler)
		return mux, nil
	}
	// Org-less root: the ops floor the default handler used to provide
	// (probes are unauthenticated by design and carry public CORS so the
	// admin panel's health strip works from any origin, api.go's
	// reasoning), the default server's unauthenticated intake routes
	// that only exist on its handler, and a structured 404 for
	// everything else - the root is nobody's org.
	mux.HandleFunc("/healthz", publicCORS(http.MethodGet, h.handleHubHealthz))
	mux.HandleFunc("/readyz", publicCORS(http.MethodGet, h.handleHubReadyz))
	mux.HandleFunc("GET /metrics", h.handleHubMetrics)
	mux.HandleFunc("/api/invite-requests", publicCORS(http.MethodPost, h.Default.handleCreateInviteRequest))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, typedErr(http.StatusNotFound, clierr.Error{
			Code: "no_default_org", Field: "path",
			Message:    "this control plane has no root-mounted org - every org lives at /o/<name>/",
			Suggestion: "GET /api/orgs lists orgs; point clients at <host>/o/<org> (e.g. runko auth login --runkod-url <host>/o/<org>)",
		}))
	})
	return mux, nil
}

// handleHubHealthz is the org-less ops floor: 200 when the hub process
// is serving. Liveness only, mirroring the default server's contract
// (api.go) minus the repo stat - an org-less hub has no root repo.
func (h *OrgHub) handleHubHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleHubReadyz adds the dependency probe: the account/directory store
// must answer (Postgres in a durable deployment).
func (h *OrgHub) handleHubReadyz(w http.ResponseWriter, r *http.Request) {
	if err := h.Default.Store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable", "reason": fmt.Sprintf("store: %v", err),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleHubMetrics is the org-less exposition: process-level gauges only
// (open-changes counts are org-scoped and live on each org's own
// /o/<name>/metrics... which does not exist; per-org metrics are a
// follow-up when something needs them).
func (h *OrgHub) handleHubMetrics(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	n := len(h.orgs)
	h.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP runkod_up Whether the daemon is serving.\n# TYPE runkod_up gauge\nrunkod_up 1\n")
	fmt.Fprintf(w, "# HELP runkod_orgs_mounted Orgs currently mounted at /o/<name>/.\n# TYPE runkod_orgs_mounted gauge\nrunkod_orgs_mounted %d\n", n)
}

// handleSignup is the org-aware sign-up: every account arrives INTO an
// org - either creating a new one (caller becomes its admin) or joining
// an existing one as a member. Join is currently open to anyone who can
// sign up at all (the deployment's invite code is the only gate); the
// recorded follow-up is per-org email invitations, at which point join
// stops being open. The org half is validated BEFORE the account is
// created so a rejected org never strands a half-registered account.
func (h *OrgHub) handleSignup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
		Code     string `json:"code"`
		Org      string `json:"org"`
		OrgMode  string `json:"org_mode"` // "create" | "join"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "bad_request", Message: "body must be JSON: {name, password, code?, org, org_mode}",
		}))
		return
	}
	req.Org = strings.TrimSpace(req.Org)
	if req.Org == "" {
		suggestion := `{"org": "<name>", "org_mode": "create"|"join"}`
		if h.DefaultOrgName != "" {
			suggestion += fmt.Sprintf(" - the shared org %q is always joinable", h.DefaultOrgName)
		}
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_org", Field: "org",
			Message:    "an account belongs to an org: name one to create or join",
			Suggestion: suggestion,
		}))
		return
	}
	switch req.OrgMode {
	case "create":
		if !h.AllowOrgCreate {
			writeAPIError(w, typedErr(http.StatusForbidden, clierr.Error{
				Code: "org_create_disabled", Field: "org",
				Message:    "org creation is not enabled on this control plane",
				Suggestion: "join an existing org instead (org_mode: join)",
			}))
			return
		}
		if !orgNamePattern.MatchString(req.Org) || reservedOrgNames[req.Org] {
			writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
				Code: "invalid_org_name", Field: "org",
				Message:    fmt.Sprintf("%q is not a valid org name", req.Org),
				Suggestion: "1-39 chars: lowercase letters, digits, dashes; must start with a letter",
			}))
			return
		}
		if req.Org == h.DefaultOrgName || h.knownOrg(req.Org) {
			writeAPIError(w, typedErr(http.StatusConflict, clierr.Error{
				Code: "org_exists", Field: "org",
				Message:    fmt.Sprintf("an org named %q already exists", req.Org),
				Suggestion: "pick a different name, or join it (org_mode: join)",
			}))
			return
		}
	case "join":
		if !h.knownOrg(req.Org) {
			writeAPIError(w, typedErr(http.StatusNotFound, clierr.Error{
				Code: "unknown_org", Field: "org",
				Message:    fmt.Sprintf("no org named %q to join", req.Org),
				Suggestion: "check the spelling with whoever invited you, or create it (org_mode: create)",
			}))
			return
		}
	default:
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_org_mode", Field: "org_mode",
			Message: `org_mode must be "create" (new org, you become admin) or "join" (existing org)`,
		}))
		return
	}

	// EnsureOrg before the account half: per-org account rows (migration
	// 0017) reference their org row, and the DEFAULT org has a serving
	// surface but (in mem mode) no directory row until someone joins it.
	// For create-mode the row is also what makes an interrupted assembly
	// self-heal: boot's LoadExisting mounts every directory row.
	if err := h.Directory.EnsureOrg(r.Context(), req.Org); err != nil {
		writeAPIError(w, internalErr(err))
		return
	}
	// Idempotent recovery (finding #44): an interrupted create-mode signup
	// used to strand its account - real, valid, member of nothing, and
	// 409ed on every retry. Re-presenting the SAME name+password now
	// recovers instead: the account half no-ops and the org half runs.
	recovered, apiErr := h.Default.signupOrRecoverCore(r.Context(), signupRequest{Name: req.Name, Password: req.Password, Code: req.Code, org: req.Org})
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	var role string
	switch req.OrgMode {
	case "create":
		if apiErr := h.createOrg(r.Context(), req.Org, req.Name, true); apiErr != nil {
			// Account exists, org lost a race (or infra failed): report it
			// honestly - the account is real, and retrying this same
			// signup recovers it (no fresh strand either way).
			state := "was created"
			if recovered {
				state = "already exists"
			}
			apiErr.Err.Message = fmt.Sprintf("account %q %s, but the org was not created: %s", req.Name, state, apiErr.Err.Message)
			writeAPIError(w, apiErr)
			return
		}
		role = "admin"
	case "join":
		// A recovered re-join must never demote: an existing membership
		// (an admin re-presenting their credential) keeps its role.
		if existing, member, err := h.Directory.OrgMemberRole(r.Context(), req.Org, req.Name); err == nil && member {
			role = existing
			break
		}
		if err := h.Directory.UpsertOrgMember(r.Context(), req.Org, req.Name, "member"); err != nil {
			writeAPIError(w, internalErr(err))
			return
		}
		role = "member"
	}
	writeJSON(w, http.StatusCreated, map[string]any{"name": req.Name, "org": h.orgInfoFor(req.Org, role)})
}

// handleAuthConfig extends the default server's discovery config with the
// org-creation bit so the sign-up form knows whether to offer it.
func (h *OrgHub) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"signup_enabled":          h.Default.AllowSignup,
		"code_required":           h.Default.AllowSignup && h.Default.SignupCode != "",
		"org_create_enabled":      h.AllowOrgCreate,
		"invite_requests_enabled": h.Default.AllowInviteRequests,
	})
}

// byMethod dispatches on the HTTP method (405 otherwise) - the routes
// above stay method-less so OPTIONS preflights reach rpcMiddleware.
func byMethod(handlers map[string]http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if next, ok := handlers[r.Method]; ok {
			next(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
}

func (h *OrgHub) routeOrg(w http.ResponseWriter, r *http.Request, defaultHandler http.Handler) {
	name := r.PathValue("org")
	var target http.Handler
	if name == h.DefaultOrgName && h.DefaultOrgName != "" {
		target = defaultHandler
	} else {
		h.mu.Lock()
		target = h.orgs[name]
		h.mu.Unlock()
	}
	h.mu.Lock()
	isArchived := h.archived[name]
	h.mu.Unlock()
	// These two early answers short-circuit BEFORE any org server's
	// CORS-setting middleware, so they must speak CORS themselves: the
	// login page asks /o/<org>/api/whoami cross-origin in the dev loop
	// with an Authorization header, which means a PREFLIGHT first - an
	// OPTIONS answered 404 (or any answer without the allow headers)
	// makes the browser report the whole exchange as an opaque "Failed
	// to fetch" instead of the mapped "no org named…" (found by
	// web/scripts/signin-smoke.mjs E3). Mirror rpcMiddleware's preflight
	// exactly, then let the real request read the structured refusal.
	// Discloses nothing new: the same answers are already readable
	// same-origin and from every non-browser client.
	if isArchived || target == nil {
		h2 := w.Header()
		h2.Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			h2.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			h2.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Connect-Protocol-Version, Connect-Timeout-Ms")
			h2.Set("Access-Control-Max-Age", "7200")
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if isArchived {
		writeAPIError(w, typedErr(http.StatusGone, clierr.Error{
			Code: "org_archived", Field: "org",
			Message:    fmt.Sprintf("org %q is archived - its repo is kept, its surface is closed", name),
			Suggestion: "an operator can restore it: POST /api/admin/orgs/" + name + "/unarchive",
		}))
		return
	}
	if target == nil {
		writeAPIError(w, typedErr(http.StatusNotFound, clierr.Error{
			Code: "unknown_org", Field: "org",
			Message:    fmt.Sprintf("no org named %q on this control plane", name),
			Suggestion: "GET /api/orgs lists yours; POST /api/orgs creates one (if enabled)",
		}))
		return
	}
	http.StripPrefix("/o/"+name, target).ServeHTTP(w, r)
}

// genesisFiles renders a created org's seed tree (§6.10): the minimal
// state that makes a fresh org immediately usable instead of a bare unborn
// trunk. A root manifest, so every path resolves an owning project and
// `workspace create --project repo` works before any real project exists;
// OWNERS naming the creator, so §7.3 inheritance covers every path and the
// default-deny posture resolves a policy from day one (uploader consent,
// api.go ownerRequirements, is what lets the solo creator actually land);
// AGENTS.md, so a coding agent pointed at a fresh clone knows the verbs
// (§8.8), plus the same teaching as a loadable agent skill at
// agentsmd.SkillPath, so skill-loading harnesses pull it into context at
// the moment of a change instead of hoping the agent read AGENTS.md;
// CONTRIBUTING.md, because §6.9 promises the generated repo shows
// the three commands that matter. All of it is ordinary tree content the
// org evolves or deletes through ordinary Changes (tree-as-truth, §10.3).
func genesisFiles(orgName, creator, trunkRef string) []core.FileChange {
	rootManifest := "# Root glue project, seeded at org creation (§6.10). It owns every path\n" +
		"# no deeper PROJECT.yaml claims, so every change resolves a merge policy;\n" +
		"# carve real projects out of it with `runko project create`.\n" +
		"schema: project/v1\n" +
		"name: repo\n" +
		"type: other\n"
	owners := "# Path ownership (§7.3): the nearest OWNERS file up the tree applies\n" +
		"# wherever a PROJECT.yaml names no owners itself. Seeded with the org's\n" +
		"# creator - add teammates as they join.\n" +
		creator + "\n"
	contributing := "# Contributing to " + orgName + "\n\n" +
		"Trunk (`" + trunkRef + "`) is closed to direct push: work lands as reviewed\n" +
		"Changes (§6.9). The three commands that matter:\n\n" +
		"    runko change create -m \"<what and why>\"   # commit your work as one Change\n" +
		"    runko change push                          # submit it (and its stack) for review\n" +
		"    runko change land --change <Change-Id>     # rebase-land once its gates are green\n\n" +
		"No runko CLI? Plain git works end to end: `git push origin HEAD:refs/for/" + trunkRef + "`\n" +
		"creates the same Change, and the server's reply names your next step at\n" +
		"every push. `runko doctor` checks a checkout and prints the cheat-sheet;\n" +
		"AGENTS.md (alongside this file) teaches coding agents the same loop.\n"
	return []core.FileChange{
		{Path: "PROJECT.yaml", Content: []byte(rootManifest)},
		{Path: "OWNERS", Content: []byte(owners)},
		{Path: "AGENTS.md", Content: []byte(agentsmd.Generate())},
		{Path: agentsmd.SkillPath, Content: []byte(agentsmd.GenerateSkill())},
		{Path: "CONTRIBUTING.md", Content: []byte(contributing)},
	}
}

// seedGenesisCommit writes genesisFiles as a created org's first trunk
// commit, directly - not through the receive funnel, because it IS the
// org's initial state: it exists before the org is announced to anyone,
// so there is no trunk to close and no reviewer to ask (the same standing
// as `git init`'s unborn branch). A born trunk is left strictly alone -
// that makes re-assembly after a crashed create (finding #44's recovery
// re-runs createOrg) a no-op instead of a history rewrite.
func seedGenesisCommit(repoDir, trunkRef, orgName, creator string) error {
	gstore := gitstore.New(repoDir)
	trunk := "refs/heads/" + trunkRef
	if _, err := gstore.ResolveRef(trunk); err == nil {
		return nil
	}
	rev, err := gstore.CommitOverlay("", core.Overlay{Changes: genesisFiles(orgName, creator, trunkRef)}, core.CommitMeta{
		Message: fmt.Sprintf("org genesis: root manifest, OWNERS (%s), AGENTS.md, agent skill, CONTRIBUTING.md (§6.10)", creator),
	})
	if err != nil {
		return fmt.Errorf("genesis commit: %w", err)
	}
	if err := gstore.UpdateRef(trunk, rev, nil); err != nil {
		return fmt.Errorf("genesis ref: %w", err)
	}
	return nil
}

// CreateOrg assembles a brand-new org end to end: repo + hook + genesis +
// store + server + workers + creator membership. Also the boot-time reload
// path (creator == "" and requireNew == false re-attaches existing orgs).
func (h *OrgHub) createOrg(ctx context.Context, name, creator string, requireNew bool) *apiError {
	if !orgNamePattern.MatchString(name) || reservedOrgNames[name] {
		return typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_org_name", Field: "name",
			Message:    fmt.Sprintf("%q is not a valid org name", name),
			Suggestion: "1-39 chars: lowercase letters, digits, dashes; must start with a letter",
		})
	}
	h.mu.Lock()
	_, exists := h.orgs[name]
	h.mu.Unlock()
	if name == h.DefaultOrgName || (exists && requireNew) {
		return typedErr(http.StatusConflict, clierr.Error{
			Code: "org_exists", Field: "name",
			Message:    fmt.Sprintf("an org named %q already exists", name),
			Suggestion: "pick a different name",
		})
	}
	if exists {
		return nil
	}

	repoDir := h.repoDirFor(name)
	if err := EnsureBareRepo(repoDir, h.Default.TrunkRef); err != nil {
		return internalErr(err)
	}
	if err := InstallPreReceiveHook(repoDir, h.SelfURL+"/o/"+name, h.Default.Token); err != nil {
		return internalErr(err)
	}
	// Genesis (§6.10): a creator-made org is born with a usable trunk.
	// creator == "" is the boot-reload path (org exists; its trunk is
	// whatever history it has) and the anonymous deploy token (no one to
	// seed OWNERS with) - both keep the bare-repo behavior.
	if creator != "" {
		if err := seedGenesisCommit(repoDir, h.Default.TrunkRef, name, creator); err != nil {
			return internalErr(err)
		}
	}
	if err := h.Directory.EnsureOrg(ctx, name); err != nil {
		return internalErr(err)
	}
	store, err := h.NewOrgStore(ctx, name)
	if err != nil {
		return internalErr(err)
	}
	server, err := h.NewOrgServer(name, repoDir, store)
	if err != nil {
		return internalErr(err)
	}
	server.OrgName = name
	server.Directory = h.Directory
	server.SettingsOrg = name
	handler, err := server.Handler()
	if err != nil {
		return internalErr(err)
	}
	if creator != "" {
		if err := h.Directory.UpsertOrgMember(ctx, name, creator, "admin"); err != nil {
			return internalErr(err)
		}
	}
	if h.StartOrgWorkers != nil {
		h.StartOrgWorkers(h.ctx(), name, store)
	}

	h.mu.Lock()
	if h.orgs == nil {
		h.orgs = map[string]http.Handler{}
	}
	if _, raced := h.orgs[name]; raced && requireNew {
		h.mu.Unlock()
		return typedErr(http.StatusConflict, clierr.Error{
			Code: "org_exists", Field: "name", Message: fmt.Sprintf("an org named %q already exists", name),
		})
	}
	h.orgs[name] = handler
	h.mu.Unlock()
	return nil
}

// LoadExisting re-attaches every org the directory knows about - the
// boot path for durable deployments. The default org's own row (created
// by the store bootstrap) is skipped: it is already mounted at root.
func (h *OrgHub) LoadExisting(ctx context.Context) ([]string, error) {
	records, err := h.Directory.ListOrgRecords(ctx)
	if err != nil {
		return nil, err
	}
	var loaded []string
	for _, rec := range records {
		if rec.Name == h.DefaultOrgName {
			continue
		}
		// Archived orgs are mounted too (unarchive must route without a
		// restart) - routeOrg answers 410 for them until then.
		if apiErr := h.createOrg(ctx, rec.Name, "", false); apiErr != nil {
			return loaded, fmt.Errorf("reload org %q: %s", rec.Name, apiErr.Err.Message)
		}
		if rec.Archived {
			h.mu.Lock()
			if h.archived == nil {
				h.archived = map[string]bool{}
			}
			h.archived[rec.Name] = true
			h.mu.Unlock()
		}
		loaded = append(loaded, rec.Name)
	}
	return loaded, nil
}

// hubCaller authenticates a hub API request - GLOBALLY, without any org's
// membership gate ("who are you" is server-global; the per-org gates
// answer "what may you reach") - and applies the hub-level rules: agents
// never manage orgs (§8.7 - org creation is exactly the kind of
// blast-radius action agent policy exists to fence), and bot lanes are
// land-only credentials.
func (h *OrgHub) hubCaller(r *http.Request) (caller, *apiError) {
	c := h.Default.callerForAuthHeaderGlobal(r.Header.Get("Authorization"))
	if !c.ok {
		return c, typedErr(http.StatusUnauthorized, clierr.Error{Code: "unauthorized", Message: "credentials required"})
	}
	if c.principal != nil && c.principal.IsAgent {
		return c, typedErr(http.StatusForbidden, clierr.Error{
			Code: "agent_denied", Message: "agent principals may not manage orgs (§8.7)",
		})
	}
	if c.lane != nil {
		return c, typedErr(http.StatusForbidden, clierr.Error{
			Code: "lane_denied", Message: "bot-lane tokens may not manage orgs",
		})
	}
	return c, nil
}

// isOperator: the anonymous deploy token and flag-configured HUMAN
// principals are operator-level - server-wide, membership-exempt.
// (hubCaller already rejected bot lanes.) Agents are NEVER operators,
// whatever minted them: an ephemeral task identity (agentprincipal.go)
// reading as operator was a real escalation - org creation, member
// management, membership exemption - found live on the feature's first
// prod smoke, and a flag-config ;agent principal had the same hole from
// the start.
func isOperator(c caller) bool {
	if c.principal == nil {
		return true
	}
	return !c.principal.Stored && !c.principal.IsAgent
}

type orgInfo struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	APIBase string `json:"api_base"`
	GitURL  string `json:"git_url"`
	Default bool   `json:"default"`
}

func (h *OrgHub) orgInfoFor(name, role string) orgInfo {
	if name == h.DefaultOrgName {
		// The default org is served at root (historical layout) AND at
		// /o/<name>/; advertise the root form - every existing remote
		// and CI config uses it.
		return orgInfo{Name: name, Role: role, APIBase: "", GitURL: "/" + RepoMountName(h.Default.RepoDir), Default: true}
	}
	// Advertise the org-named mount (api.go repoMount): `git clone` of
	// this URL lands in a folder named after the org, not "repo". The
	// on-disk /o/<name>/repo.git path stays served for existing remotes.
	return orgInfo{Name: name, Role: role, APIBase: "/o/" + name, GitURL: "/o/" + name + "/" + name + ".git"}
}

func (h *OrgHub) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
	c, apiErr := h.hubCaller(r)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	if !h.AllowOrgCreate {
		writeAPIError(w, typedErr(http.StatusForbidden, clierr.Error{
			Code: "org_create_disabled", Field: "orgs",
			Message:    "org creation is not enabled on this control plane",
			Suggestion: "an operator must start runkod with --allow-org-create",
		}))
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "bad_request", Message: "body must be JSON: {name}",
		}))
		return
	}
	creator := ""
	if c.principal != nil {
		creator = c.principal.Name
	}
	if apiErr := h.createOrg(r.Context(), body.Name, creator, true); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	// Per-org identity: the stored creator needs an account row IN the new
	// org to ever sign into it. Clone the credential they authenticated
	// WITH (verified against its source org's hash - never a same-named
	// stranger's row) into the new org's account space.
	if c.principal != nil && c.principal.Stored {
		if _, pass, ok := r.BasicAuth(); ok {
			if rows, err := h.Directory.ListPrincipalOrgs(r.Context(), creator); err == nil {
				for _, sp := range rows {
					if !verifyCredential(pass, sp.CredentialHash) {
						continue
					}
					if err := h.Default.Store.CreatePrincipal(r.Context(), body.Name, creator, sp.CredentialHash); err != nil {
						writeAPIError(w, internalErr(fmt.Errorf("org created, but copying your account into it failed: %w", err)))
						return
					}
					break
				}
			}
		}
	}
	role := "admin"
	if creator == "" {
		role = "operator"
	}
	writeJSON(w, http.StatusCreated, h.orgInfoFor(body.Name, role))
}

func (h *OrgHub) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	// §15.2 public_read discovery: a request with NO credentials lists
	// exactly the public orgs (role "anonymous") so the web sign-in page
	// can offer read-only browsing. Wrong credentials still 401 below -
	// never a silent downgrade.
	if r.Header.Get("Authorization") == "" {
		names, err := h.Directory.ListOrgNames(r.Context())
		if err != nil {
			writeAPIError(w, internalErr(err))
			return
		}
		if h.DefaultOrgName != "" {
			names = append(names, h.DefaultOrgName)
		}
		out := []orgInfo{}
		seen := map[string]bool{}
		for _, n := range names {
			h.mu.Lock()
			archived := h.archived[n]
			h.mu.Unlock()
			if seen[n] || archived {
				continue
			}
			seen[n] = true
			if settings, err := h.Directory.GetOrgSettings(r.Context(), n); err == nil && settings.PublicRead {
				out = append(out, h.orgInfoFor(n, "anonymous"))
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"orgs": out})
		return
	}
	c, apiErr := h.hubCaller(r)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	// Org-scoped sessions (2026-07-09): a stored account sees exactly its
	// MEMBERSHIPS - no unconditional default-org row, no enumeration of
	// anything it doesn't belong to. Operators (server config) still see
	// everything: they run the place.
	out := []orgInfo{}
	if isOperator(c) {
		records, err := h.Directory.ListOrgRecords(r.Context())
		if err != nil {
			writeAPIError(w, internalErr(err))
			return
		}
		seen := map[string]bool{}
		for _, rec := range records {
			seen[rec.Name] = true
			if rec.Archived {
				continue // the selector lists LIVE orgs; admin panel shows the rest
			}
			out = append(out, h.orgInfoFor(rec.Name, "operator"))
		}
		// Mem-mode directories only know orgs registered this process;
		// the hub's map + the default org are authoritative for routing.
		h.mu.Lock()
		for n := range h.orgs {
			if !seen[n] {
				seen[n] = true
				out = append(out, h.orgInfoFor(n, "operator"))
			}
		}
		h.mu.Unlock()
		if h.DefaultOrgName != "" && !seen[h.DefaultOrgName] {
			out = append(out, h.orgInfoFor(h.DefaultOrgName, "operator"))
		}
	} else {
		memberships, err := h.Directory.ListOrgMemberships(r.Context(), c.principal.Name)
		if err != nil {
			writeAPIError(w, internalErr(err))
			return
		}
		_, pass, _ := r.BasicAuth()
		for _, m := range memberships {
			// Per-org accounts: a membership row counts only when THIS
			// credential verifies against that org's own account - the
			// same name in another org is someone else's account, and
			// their orgs must not leak into this caller's selector.
			sp, found, err := h.Directory.GetStoredPrincipal(r.Context(), m.Org, c.principal.Name)
			if err != nil || !found {
				continue
			}
			if !(h.Default.credCache.hit(credKey(m.Org, sp.Name), pass) || verifyCredential(pass, sp.CredentialHash)) {
				continue
			}
			h.Default.credCache.remember(credKey(m.Org, sp.Name), pass)
			out = append(out, h.orgInfoFor(m.Org, m.Role))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, map[string]any{"orgs": out})
}

func (h *OrgHub) handleListOrgMembers(w http.ResponseWriter, r *http.Request) {
	orgName, _, ok := h.requireOrg(w, r, false)
	if !ok {
		return
	}
	members, err := h.Directory.ListOrgMembers(r.Context(), orgName)
	if err != nil {
		writeAPIError(w, internalErr(err))
		return
	}
	if members == nil {
		members = []OrgMember{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"org": orgName, "members": members})
}

func (h *OrgHub) handleRemoveOrgMember(w http.ResponseWriter, r *http.Request) {
	orgName, _, ok := h.requireOrg(w, r, true)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if _, member, err := h.Directory.OrgMemberRole(r.Context(), orgName, name); err != nil {
		writeAPIError(w, internalErr(err))
		return
	} else if !member {
		writeAPIError(w, typedErr(http.StatusNotFound, clierr.Error{
			Code: "not_a_member", Field: "name",
			Message: fmt.Sprintf("%q is not a member of %q", name, orgName),
		}))
		return
	}
	if err := h.Directory.RemoveOrgMember(r.Context(), orgName, name); err != nil {
		writeAPIError(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"org": orgName, "removed": name})
}

func (h *OrgHub) handleGetOrgSettings(w http.ResponseWriter, r *http.Request) {
	orgName, _, ok := h.requireOrg(w, r, false)
	if !ok {
		return
	}
	settings, err := h.Directory.GetOrgSettings(r.Context(), orgName)
	if err != nil {
		writeAPIError(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"org": orgName, "settings": settings})
}

const (
	maxOrgDescription    = 1000
	maxOrgRequiredChecks = 64
)

func (h *OrgHub) handlePutOrgSettings(w http.ResponseWriter, r *http.Request) {
	orgName, _, ok := h.requireOrg(w, r, true)
	if !ok {
		return
	}
	var settings OrgSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "bad_request", Message: "body must be JSON: {description?, global_required_checks?}",
		}))
		return
	}
	if len(settings.Description) > maxOrgDescription {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "description_too_long", Field: "description",
			Message: fmt.Sprintf("description is capped at %d characters", maxOrgDescription),
		}))
		return
	}
	// Normalize check names: trimmed, no empties, no duplicates - these
	// feed straight into the §13.5 merge gate.
	seen := map[string]bool{}
	var checks []string
	for _, c := range settings.GlobalRequiredChecks {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		checks = append(checks, c)
	}
	if len(checks) > maxOrgRequiredChecks {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "too_many_checks", Field: "global_required_checks",
			Message: fmt.Sprintf("at most %d org-required checks", maxOrgRequiredChecks),
		}))
		return
	}
	settings.GlobalRequiredChecks = checks
	switch settings.RevalidationPolicy {
	case "", string(land.RevalidationConflictOnly), string(land.RevalidationAffectedIntersection), string(land.RevalidationAlways):
		// valid tiers (§13.5, 2026-07-15); "" defers to the daemon flag
	default:
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_revalidation_policy", Field: "revalidation_policy",
			Message:    fmt.Sprintf("%q is not a revalidation tier", settings.RevalidationPolicy),
			Suggestion: "one of conflict-only | affected-intersection | always (never is the admin force override, not a policy)",
		}))
		return
	}
	// public_read + visibility:restricted are mutually exclusive until
	// §12.3 Phase B (per-principal filtered fetch) exists - fail closed at
	// enable time rather than leaking a restricted project anonymously.
	if settings.PublicRead {
		repoDir := h.repoDirFor(orgName)
		if orgName == h.DefaultOrgName && h.Default != nil {
			repoDir = h.Default.RepoDir
		}
		if restricted, err := restrictedProjects(repoDir); err != nil {
			writeAPIError(w, internalErr(err))
			return
		} else if len(restricted) > 0 {
			writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
				Code: "restricted_projects_present", Field: "public_read",
				Message:    fmt.Sprintf("public_read cannot be enabled while restricted projects exist: %s", strings.Join(restricted, ", ")),
				Suggestion: "remove visibility: restricted from those manifests, or keep the org private (§15.2)",
			}))
			return
		}
	}
	// github_mirror_repo is connect-owned (POST /api/github/connect):
	// the PUT replaces settings wholesale, so carry the wiring forward
	// or every settings-page save would silently disconnect the mirror.
	if existing, err := h.Directory.GetOrgSettings(r.Context(), orgName); err == nil {
		settings.GithubMirrorRepo = existing.GithubMirrorRepo
	}
	if err := h.Directory.UpdateOrgSettings(r.Context(), orgName, settings); err != nil {
		writeAPIError(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"org": orgName, "settings": settings})
}

// knownOrg reports whether name is a routable org - the default org
// counts: its membership rows carry admin roles for the settings page
// even though its serving surface stays ungated.
func (h *OrgHub) knownOrg(name string) bool {
	if name == h.DefaultOrgName && h.DefaultOrgName != "" {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, exists := h.orgs[name]
	return exists
}

// orgAccess resolves what the caller may do in one org: operators (and
// the anonymous deploy token) act everywhere; stored accounts act per
// their membership role. The default org is no exception (org-scoped
// sessions, 2026-07-09): non-members see and reach nothing.
func (h *OrgHub) orgAccess(r *http.Request, c caller, orgName string) (role string, canRead, canAdmin bool, err error) {
	if isOperator(c) {
		return "operator", true, true, nil
	}
	role, member, err := h.Directory.OrgMemberRole(r.Context(), orgName, c.principal.Name)
	if err != nil {
		return "", false, false, err
	}
	if !member {
		return "", false, false, nil
	}
	return role, true, role == "admin", nil
}

// requireOrg 404s unknown orgs and returns the caller's access; adminOnly
// additionally demands write authority.
func (h *OrgHub) requireOrg(w http.ResponseWriter, r *http.Request, adminOnly bool) (orgName string, c caller, ok bool) {
	c, apiErr := h.hubCaller(r)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return "", c, false
	}
	orgName = r.PathValue("org")
	if !h.knownOrg(orgName) {
		writeAPIError(w, typedErr(http.StatusNotFound, clierr.Error{
			Code: "unknown_org", Field: "org", Message: fmt.Sprintf("no org named %q", orgName),
		}))
		return "", c, false
	}
	_, canRead, canAdmin, err := h.orgAccess(r, c, orgName)
	if err != nil {
		writeAPIError(w, internalErr(err))
		return "", c, false
	}
	if !canRead {
		writeAPIError(w, orgDeniedErr(orgName))
		return "", c, false
	}
	if adminOnly && !canAdmin {
		writeAPIError(w, typedErr(http.StatusForbidden, clierr.Error{
			Code: "not_org_admin", Field: "org",
			Message:    fmt.Sprintf("only admins of %q (or an operator) may do this", orgName),
			Suggestion: "ask an org admin or an operator",
		}))
		return "", c, false
	}
	return orgName, c, true
}

func (h *OrgHub) handleAddOrgMember(w http.ResponseWriter, r *http.Request) {
	orgName, _, ok := h.requireOrg(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
		Role string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "bad_request", Message: "body must be JSON: {name, role?}",
		}))
		return
	}
	if body.Role == "" {
		body.Role = "member"
	}
	// "releaser" (§14.10.3, stage 17): a member who may also write
	// refs/tags/* and cut releases when the org enforces tag policy -
	// release rights without admin rights.
	if body.Role != "member" && body.Role != "admin" && body.Role != "releaser" {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_role", Field: "role", Message: `role must be "member", "admin", or "releaser"`,
		}))
		return
	}
	// The account must exist IN THIS ORG (per-org identity, migration
	// 0017): membership is a role on one of the org's own accounts, and
	// the same name in another org is someone else's account.
	if _, found, err := h.Directory.GetStoredPrincipal(r.Context(), orgName, body.Name); err != nil {
		writeAPIError(w, internalErr(err))
		return
	} else if !found {
		writeAPIError(w, typedErr(http.StatusNotFound, clierr.Error{
			Code: "unknown_principal", Field: "name",
			Message:    fmt.Sprintf("no account named %q in org %q", body.Name, orgName),
			Suggestion: fmt.Sprintf("they need to sign up into %q first (org_mode: join)", orgName),
		}))
		return
	}
	// EnsureOrg first, same as the signup join path: the DEFAULT org is
	// routable without a directory row (mem mode) until someone joins it,
	// and upserting a membership into a rowless org is a 500.
	if err := h.Directory.EnsureOrg(r.Context(), orgName); err != nil {
		writeAPIError(w, internalErr(err))
		return
	}
	if err := h.Directory.UpsertOrgMember(r.Context(), orgName, body.Name, body.Role); err != nil {
		writeAPIError(w, internalErr(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"org": orgName, "name": body.Name, "role": body.Role})
}

// TrimGitSuffix is a tiny helper for deriving the default org name from
// a repo mount ("monorepo.git" -> "monorepo").
func TrimGitSuffix(mount string) string {
	return strings.TrimSuffix(mount, ".git")
}

// ---- deployment admin surface (operator-only) --------------------------

// requireOperator: the admin panel is for whoever RUNS the deployment -
// the anonymous deploy token and flag-configured principals. Org admins
// administer their org via its settings page, not this.
func (h *OrgHub) requireOperator(w http.ResponseWriter, r *http.Request) (caller, bool) {
	c, apiErr := h.hubCaller(r)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return c, false
	}
	if !isOperator(c) {
		writeAPIError(w, typedErr(http.StatusForbidden, clierr.Error{
			Code: "operator_only", Field: "admin",
			Message:    "this is the deployment admin surface - operator credentials only",
			Suggestion: "sign in with an operator principal (--principal) or the deploy token",
		}))
		return c, false
	}
	return c, true
}

// handleAdminWhoami is the admin panel's dedicated sign-in check: one
// round trip that both validates the credential and answers operator-
// ness. Operators are server-global, so unlike the org-scoped
// /o/<org>/api/whoami there is no org in this flow at all. 401 = wrong
// credential; 403 = valid but not an operator; 200 = in.
func (h *OrgHub) handleAdminWhoami(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	name, anonymous := "", true
	if c.principal != nil {
		name, anonymous = c.principal.Name, false
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "anonymous": anonymous, "operator": true})
}

// handleAdminCreateOrg is org creation on the admin surface. Unlike the
// self-service POST /api/orgs it is NOT gated by --allow-org-create:
// that flag scopes what signup accounts may do, and the operator is
// whoever would flip it. No credential cloning either - operators are
// config principals (never stored), membership-exempt everywhere.
func (h *OrgHub) handleAdminCreateOrg(w http.ResponseWriter, r *http.Request) {
	c, ok := h.requireOperator(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "bad_request", Message: "body must be JSON: {name}",
		}))
		return
	}
	creator := ""
	if c.principal != nil {
		creator = c.principal.Name
	}
	if apiErr := h.createOrg(r.Context(), body.Name, creator, true); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusCreated, h.orgInfoFor(body.Name, "operator"))
}

type adminOrgRow struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Members     []string `json:"members"`
	Archived    bool     `json:"archived"`
	Default     bool     `json:"default"`
}

func (h *OrgHub) handleAdminOrgs(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireOperator(w, r); !ok {
		return
	}
	records, err := h.Directory.ListOrgRecords(r.Context())
	if err != nil {
		writeAPIError(w, internalErr(err))
		return
	}
	// The default org is a row like any other here; mem-mode directories
	// may not carry it, so union it in.
	names := map[string]bool{}
	for _, rec := range records {
		names[rec.Name] = true
	}
	if h.DefaultOrgName != "" && !names[h.DefaultOrgName] {
		records = append([]OrgRecord{{Name: h.DefaultOrgName}}, records...)
	}
	rows := []adminOrgRow{}
	for _, rec := range records {
		row := adminOrgRow{Name: rec.Name, Archived: rec.Archived, Default: rec.Name == h.DefaultOrgName}
		if settings, err := h.Directory.GetOrgSettings(r.Context(), rec.Name); err == nil {
			row.Description = settings.Description
		}
		if members, err := h.Directory.ListOrgMembers(r.Context(), rec.Name); err == nil {
			for _, m := range members {
				row.Members = append(row.Members, m.Name)
			}
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"orgs": rows})
}

func (h *OrgHub) archiveHandler(archive bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := h.requireOperator(w, r); !ok {
			return
		}
		orgName := r.PathValue("org")
		if orgName == h.DefaultOrgName {
			writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
				Code: "default_org_immutable", Field: "org",
				Message: "the default org is this deployment's root mount - it cannot be archived",
			}))
			return
		}
		if !h.knownOrg(orgName) {
			writeAPIError(w, typedErr(http.StatusNotFound, clierr.Error{
				Code: "unknown_org", Field: "org", Message: fmt.Sprintf("no org named %q", orgName),
			}))
			return
		}
		if err := h.Directory.SetOrgArchived(r.Context(), orgName, archive); err != nil {
			writeAPIError(w, internalErr(err))
			return
		}
		h.mu.Lock()
		if h.archived == nil {
			h.archived = map[string]bool{}
		}
		h.archived[orgName] = archive
		h.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"org": orgName, "archived": archive})
	}
}

// restrictedProjects lists trunk-manifest projects declaring
// visibility: restricted - the §15.2 public_read enable-time guard.
// An unborn trunk has no manifests and nothing to restrict.
func restrictedProjects(repoDir string) ([]string, error) {
	gstore := gitstore.New(repoDir)
	// ^{commit} forces real resolution: bare `rev-parse HEAD` on an unborn
	// trunk echoes the literal name with exit 0 instead of failing.
	rev, err := gstore.ResolveRef("HEAD^{commit}")
	if err != nil {
		return nil, nil
	}
	indexed, err := index.Scan(gstore, core.Revision(rev), nil)
	if err != nil {
		return nil, fmt.Errorf("scan projects: %w", err)
	}
	var restricted []string
	for _, p := range indexed {
		if p.Visibility == "restricted" {
			restricted = append(restricted, p.Name)
		}
	}
	return restricted, nil
}
