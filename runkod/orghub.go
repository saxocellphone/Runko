// Multi-org routing (§7.1's Org -> Monorepo model reaching the daemon,
// 2026-07-08). The schema has been multi-tenant since stage 2 (everything
// keys on org_id/monorepo_id); this closes the daemon-side gap: each org
// owns its own bare repo and gets its own Server instance - the whole
// existing surface (smart-HTTP git, REST, Connect RPC, the pre-receive
// callback) mounted unchanged under /o/<org>/. A base URL is the only
// thing a client needs, so `runko --runkod-url https://host/o/acme` and
// the web transport work against an org with zero client changes.
//
// The root-mounted repo the daemon has always served stays exactly as it
// was (the "default org", also reachable at /o/<default-name>/ for
// uniformity): existing deployments, remotes, and CI keep working. Only
// hub-created orgs are membership-gated - the default org keeps its
// historical everyone-with-a-credential behavior.
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
	GetStoredPrincipal(ctx context.Context, name string) (StoredPrincipal, bool, error)
	// EnsureOrg registers the org row (idempotent). Repo/server assembly
	// is the hub's job, not the directory's.
	EnsureOrg(ctx context.Context, name string) error
	OrgMemberRole(ctx context.Context, orgName, principal string) (role string, member bool, err error)
	UpsertOrgMember(ctx context.Context, orgName, principal, role string) error
	RemoveOrgMember(ctx context.Context, orgName, principal string) error
	ListOrgMembers(ctx context.Context, orgName string) ([]OrgMember, error)
	ListOrgMemberships(ctx context.Context, principal string) ([]OrgMembership, error)
	ListOrgNames(ctx context.Context) ([]string, error)
	// GetOrgSettings returns the zero value for an org with nothing set.
	GetOrgSettings(ctx context.Context, orgName string) (OrgSettings, error)
	UpdateOrgSettings(ctx context.Context, orgName string, settings OrgSettings) error
}

// OrgMember is one account's membership in one org.
type OrgMember struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// OrgSettings is the org settings page's storage shape (migration 0008,
// JSONB on the org row). Deliberately thin - real merge policy lives in
// the tree (§9.4); these are the org-level knobs that were daemon flags
// before multi-org made the org a first-class row.
type OrgSettings struct {
	Description string `json:"description,omitempty"`
	// GlobalRequiredChecks are required on EVERY change in this org
	// (§14.9), merged with the daemon-level --global-required-checks.
	GlobalRequiredChecks []string `json:"global_required_checks,omitempty"`
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

	mu   sync.Mutex
	orgs map[string]http.Handler
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
func (h *OrgHub) Handler() (http.Handler, error) {
	defaultHandler, err := h.Default.Handler()
	if err != nil {
		return nil, err
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
	mux.Handle("/api/orgs", h.Default.rpcMiddleware(byMethod(map[string]http.HandlerFunc{
		http.MethodGet: h.handleListOrgs, http.MethodPost: h.handleCreateOrg,
	})))
	mux.Handle("/api/orgs/{org}/members", h.Default.rpcMiddleware(byMethod(map[string]http.HandlerFunc{
		http.MethodGet: h.handleListOrgMembers, http.MethodPost: h.handleAddOrgMember,
	})))
	mux.Handle("/api/orgs/{org}/members/{name}", h.Default.rpcMiddleware(byMethod(map[string]http.HandlerFunc{
		http.MethodDelete: h.handleRemoveOrgMember,
	})))
	mux.Handle("/api/orgs/{org}/settings", h.Default.rpcMiddleware(byMethod(map[string]http.HandlerFunc{
		http.MethodGet: h.handleGetOrgSettings, http.MethodPut: h.handlePutOrgSettings,
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
	mux.Handle("/", defaultHandler)
	return mux, nil
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
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_org", Field: "org",
			Message:    "an account belongs to an org: name one to create or join",
			Suggestion: fmt.Sprintf(`{"org": "<name>", "org_mode": "create"|"join"} - the shared org %q is always joinable`, h.DefaultOrgName),
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

	if apiErr := h.Default.signupCore(r.Context(), signupRequest{Name: req.Name, Password: req.Password, Code: req.Code}); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	var role string
	switch req.OrgMode {
	case "create":
		if apiErr := h.createOrg(r.Context(), req.Org, req.Name, true); apiErr != nil {
			// Account exists, org lost a race (or infra failed): report it
			// honestly - the account is real and usable.
			apiErr.Err.Message = fmt.Sprintf("account %q was created, but the org was not: %s", req.Name, apiErr.Err.Message)
			writeAPIError(w, apiErr)
			return
		}
		role = "admin"
	case "join":
		// EnsureOrg first: the DEFAULT org has a serving surface but (in
		// mem mode) no directory row until someone joins it.
		if err := h.Directory.EnsureOrg(r.Context(), req.Org); err != nil {
			writeAPIError(w, internalErr(err))
			return
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
		"signup_enabled":     h.Default.AllowSignup,
		"code_required":      h.Default.AllowSignup && h.Default.SignupCode != "",
		"org_create_enabled": h.AllowOrgCreate,
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

// CreateOrg assembles a brand-new org end to end: repo + hook + store +
// server + workers + creator membership. Also the boot-time reload path
// (creator == "" and requireNew == false re-attaches existing orgs).
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
	names, err := h.Directory.ListOrgNames(ctx)
	if err != nil {
		return nil, err
	}
	var loaded []string
	for _, name := range names {
		if name == h.DefaultOrgName {
			continue
		}
		if apiErr := h.createOrg(ctx, name, "", false); apiErr != nil {
			return loaded, fmt.Errorf("reload org %q: %s", name, apiErr.Err.Message)
		}
		loaded = append(loaded, name)
	}
	return loaded, nil
}

// hubCaller authenticates a hub API request against the default server's
// resolver and applies the hub-level rules: agents never manage orgs
// (§8.7 - org creation is exactly the kind of blast-radius action agent
// policy exists to fence), and bot lanes are land-only credentials.
func (h *OrgHub) hubCaller(r *http.Request) (caller, *apiError) {
	c := h.Default.callerForAuthHeader(r.Header.Get("Authorization"))
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

// isOperator: the anonymous deploy token and flag-configured principals
// are operator-level - server-wide, membership-exempt. (hubCaller already
// rejected bot lanes.)
func isOperator(c caller) bool {
	return c.principal == nil || !c.principal.Stored
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
	return orgInfo{Name: name, Role: role, APIBase: "/o/" + name, GitURL: "/o/" + name + "/repo.git"}
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
	role := "admin"
	if creator == "" {
		role = "operator"
	}
	writeJSON(w, http.StatusCreated, h.orgInfoFor(body.Name, role))
}

func (h *OrgHub) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	c, apiErr := h.hubCaller(r)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	var out []orgInfo
	defaultRole := "shared"
	if isOperator(c) {
		defaultRole = "operator"
		names, err := h.Directory.ListOrgNames(r.Context())
		if err != nil {
			writeAPIError(w, internalErr(err))
			return
		}
		seen := map[string]bool{}
		for _, n := range names {
			if n == h.DefaultOrgName {
				continue
			}
			seen[n] = true
			out = append(out, h.orgInfoFor(n, "operator"))
		}
		// Mem-mode directories only know orgs created this process; the
		// hub's own map is authoritative for what is actually routable.
		h.mu.Lock()
		for n := range h.orgs {
			if !seen[n] {
				out = append(out, h.orgInfoFor(n, "operator"))
			}
		}
		h.mu.Unlock()
	} else {
		memberships, err := h.Directory.ListOrgMemberships(r.Context(), c.principal.Name)
		if err != nil {
			writeAPIError(w, internalErr(err))
			return
		}
		for _, m := range memberships {
			if m.Org == h.DefaultOrgName {
				// Listed unconditionally below - but a real membership
				// row (e.g. an admin of the shared org) names its role.
				defaultRole = m.Role
				continue
			}
			out = append(out, h.orgInfoFor(m.Org, m.Role))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	// The default org is everyone's: the shared repo this daemon has
	// always served, ungated by membership.
	if h.DefaultOrgName != "" {
		out = append([]orgInfo{h.orgInfoFor(h.DefaultOrgName, defaultRole)}, out...)
	}
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
// their membership role. The default org is readable by every valid
// credential (it is the shared repo), writable by its admins.
func (h *OrgHub) orgAccess(r *http.Request, c caller, orgName string) (role string, canRead, canAdmin bool, err error) {
	if isOperator(c) {
		return "operator", true, true, nil
	}
	role, member, err := h.Directory.OrgMemberRole(r.Context(), orgName, c.principal.Name)
	if err != nil {
		return "", false, false, err
	}
	if !member {
		return "", orgName == h.DefaultOrgName, false, nil
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
	if (adminOnly && !canAdmin) || !canRead {
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
	if body.Role != "member" && body.Role != "admin" {
		writeAPIError(w, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "invalid_role", Field: "role", Message: `role must be "member" or "admin"`,
		}))
		return
	}
	// The account must exist: membership for a name nobody registered is
	// a typo, not an invitation system.
	if _, found, err := h.Directory.GetStoredPrincipal(r.Context(), body.Name); err != nil {
		writeAPIError(w, internalErr(err))
		return
	} else if !found {
		writeAPIError(w, typedErr(http.StatusNotFound, clierr.Error{
			Code: "unknown_principal", Field: "name",
			Message:    fmt.Sprintf("no account named %q", body.Name),
			Suggestion: "they need to sign up first",
		}))
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
