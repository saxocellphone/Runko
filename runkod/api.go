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
	"net/http/cgi"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitstore"
	"github.com/saxocellphone/runko/platform/affected"
	"github.com/saxocellphone/runko/platform/checks"
	"github.com/saxocellphone/runko/platform/core"
	"github.com/saxocellphone/runko/platform/index"
	"github.com/saxocellphone/runko/platform/land"
	"github.com/saxocellphone/runko/platform/mirror"
	"github.com/saxocellphone/runko/platform/search"
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
	// Events is the §12.6 in-process live feed - one bus per org, the
	// SAME instance as Processor.Events (the receive side publishes,
	// WatchWorkspace subscribes here). Nil-safe: without it the timeline
	// rows still land, only the live poke is absent.
	Events *EventBus
	// Searcher backs GET /api/search (§8.3's search_code tool). Defaults to
	// search.NotConfiguredSearcher{} in Handler if left nil, so a daemon
	// started without --search-url still answers with a structured "not
	// configured" error rather than panicking.
	Searcher search.CodeSearcher
	// GlobalRequiredChecks are org-level check names required on EVERY
	// Change regardless of which projects it touches (§14.9 "org can define
	// global required checks, e.g. secrets-scan always"). Like the
	// Processor's RootInvalidationPatterns, this is org policy carried as
	// daemon config for now; §9.4's guard ("the tree owns policy") marks
	// both for eventual relocation into the tree.
	GlobalRequiredChecks []string
	// AllowSignup enables POST /api/signup (§15.1 self-service
	// principals; signup.go). Default off - the default-deny posture. When
	// SignupCode is also set, sign-ups must present it (a shareable invite
	// string, not a secret credential).
	AllowSignup bool
	SignupCode  string
	// AllowInviteRequests enables the public POST /api/invite-requests
	// intake (§15.1 invite requests; invite.go) - the ASK half of the
	// SignupCode gate. Default off, and wired to the default server only:
	// requests are deployment-wide, like signup itself.
	AllowInviteRequests bool
	// inviteLimiter is the intake's per-IP window (invite.go).
	inviteLimiter inviteLimiter
	// credCache amortizes PBKDF2 verification for store-backed principals
	// (credential.go) - Basic credentials arrive on EVERY request.
	credCache credCache
	// AllowUnpolicedLand disables the §28.3 stage 11c default-deny posture:
	// a Change for which NO merge policy resolves (zero required checks
	// after ci.checks + GlobalRequiredChecks, and zero owner requirements
	// for its touched paths) is NOT mergeable unless this is set. The zero
	// value is the safe production default; cmd/runkod sets it true for the
	// §9.3 Eval/dev profile (in-memory store) and behind the loud
	// --insecure-allow-unpoliced-land opt-out otherwise.
	AllowUnpolicedLand bool
	// BotLanes are §14.10.2's path-scoped auto-land grants; see BotLane.
	BotLanes []BotLane
	// Principals is §15.1's interim named-token identity registry (stage
	// 12c); see Principal. Keep Processor.Principals pointed at the same
	// slice so API-side attribution and receive-side enforcement agree on
	// who exists.
	Principals []Principal
	// Mirror is the outbound mirror worker (§18.6 M1, mirror.go). Since
	// `github connect` (2026-07-16) every server gets
	// one - unarmed (nil Remote) until flag config, a stored
	// github_mirror_repo, or the connect endpoint arms it. Provider-
	// agnostic by construction: any smart-HTTPS git host, or any git URL
	// at all without token auth.
	Mirror *MirrorWorker
	// GithubRemote builds a mirror remote for "owner/name" on the
	// deployment's GitHub host, credentialed by the daemon-level GitHub
	// App (github.go). nil when the daemon holds no App credentials -
	// POST /api/github/connect then answers a structured
	// github_app_not_configured.
	GithubRemote func(repoPath string) *mirror.Remote
	// OrgName + Directory are set on org-scoped servers built by an
	// OrgHub (orghub.go). When OrgName is non-empty, store-backed
	// accounts must be members of that org to authenticate here at all
	// (403, auth.go); operator principals, bot lanes, and the deploy
	// token stay server-wide. Empty OrgName - the root-mounted default
	// org and every pre-hub deployment - keeps the historical
	// shared-repo behavior.
	OrgName   string
	Directory Directory
	// SettingsOrg names the org whose STORED settings (org settings page;
	// Directory.GetOrgSettings) apply to this server - the default server
	// gets the default org's name (it stays membership-ungated, so this
	// is distinct from OrgName), org servers their own. Empty means flag
	// config only.
	SettingsOrg string
	// SingleUseAgentWorkspaces closes an AGENT-owned workspace the moment
	// its last open change lands or is abandoned (one workspace = one
	// task; the funnel refuses pushes into closed workspaces with a
	// create-a-fresh-one suggestion). Agents only - human workspaces stay
	// long-lived (§8.7: same clients, stricter defaults). cmd/runkod
	// defaults this ON (--single-use-agent-workspaces=false to opt out);
	// the zero value is off so existing embedders/tests are unchanged.
	SingleUseAgentWorkspaces bool
	// Now overrides the clock the §14.4.2 check-staleness comparison uses;
	// nil means time.Now (tests inject a fake clock).
	Now func() time.Time

	// LandIdentity is stamped as BOTH author and committer on every commit
	// this server lands, so trunk (and the outbound mirror that transports
	// it verbatim) carries a single canonical identity rather than whatever
	// git identity the client happened to have (§7.5; changelog
	// 2026-07-13). Per-author attribution lives in authored_by/landed_by,
	// not the git author field. The zero value falls back to
	// land.DefaultIdentity (see landIdentity); cmd/runkod wires
	// --land-identity here.
	LandIdentity land.Identity

	// Revalidation is the daemon-level §13.5 revalidation tier
	// (conflict-only | affected-intersection | always; cmd/runkod wires
	// --revalidation/RUNKO_REVALIDATION here). The zero value behaves as
	// land.RevalidationConflictOnly (the 2026-07-15 default); an org's
	// stored revalidation_policy setting overrides it - see
	// effectiveRevalidationScope.
	Revalidation land.RevalidationScope

	// affectedMu/affectedCache memoize computeAffected by (base_sha,
	// head_sha). Both key halves are commit SHAs, so an entry can never go
	// stale - §13.3's "never cached past a head_sha change" is honored BY
	// the key: a new head is simply a new entry. Without this, every
	// merge-requirements read re-walked the whole tree at head (one git
	// subprocess per directory, two more per manifest - ~400ms on the
	// dogfood repo), and the UI reads merge requirements once per listed
	// change (stage 15 dogfood: "landed/open tabs load slowly").
	affectedMu    sync.Mutex
	affectedCache map[string]affectedEntry

	// scanMu/scanCache memoize index.Scan by resolved commit SHA -
	// affectedCache's sibling, one layer down. A SHA names an immutable
	// tree, so entries never go stale; a land invalidates by changing the
	// trunk tip SHA, not by eviction. Without this, every project/browse/
	// search read re-walked the whole tree (one git subprocess per
	// directory, two more per manifest), and the web UI's project page
	// fans out to one GetProject per project (stage 15 dogfood: "the
	// project page loads slowly" - 12 concurrent RPCs × ~80 subprocess
	// spawns each, ~2.8s measured on the dogfood deployment).
	scanMu    sync.Mutex
	scanCache map[core.Revision][]index.IndexedProject

	// baseRelMu/baseRelCache memoize baseTrunkRelation (history.go) by
	// (base SHA, tip SHA) - the same never-stale keying as its siblings
	// above. One entry per open change's base per trunk position.
	baseRelMu    sync.Mutex
	baseRelCache map[string]baseRel

	// automerge is the when-ready land worker (automerge.go); nil when
	// none was started (tests, one-shot tools). KickAutomerge is the
	// nil-safe nudge the mergeability-flipping handlers call.
	automerge *AutomergeWorker
}

// indexedProjectsAt is the memoized index.Scan every read path shares. rev
// MUST be a resolved commit SHA, never a ref name - a ref-name key would
// serve yesterday's tree after the ref moves. The mutex is held across the
// scan on purpose (unlike computeAffected's): the cold case is a burst of
// identical reads - the web UI's project page issues a dozen at once - and
// single-flighting turns N scans into one scan plus N waits. Entries are
// shared across requests, so callers treat the slice as read-only - every
// existing consumer already does (they derive fresh slices).
func (s *Server) indexedProjectsAt(gstore *gitstore.Store, rev core.Revision) ([]index.IndexedProject, error) {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if indexed, ok := s.scanCache[rev]; ok {
		return indexed, nil
	}
	indexed, err := index.Scan(gstore, rev, nil)
	if err != nil {
		return nil, err
	}
	if s.scanCache == nil {
		s.scanCache = map[core.Revision][]index.IndexedProject{}
	}
	// SHA-keyed entries never expire, but browsed historical revs and
	// change heads must not grow the map forever; past ~512 just start
	// over (affectedCache's rule).
	if len(s.scanCache) >= 512 {
		s.scanCache = map[core.Revision][]index.IndexedProject{}
	}
	s.scanCache[rev] = indexed
	return indexed, nil
}

// affectedEntry is one memoized computeAffected result. Entries are shared
// across requests, so callers treat result/indexed as read-only - every
// existing consumer already does (they derive fresh slices).
type affectedEntry struct {
	result  affected.Result
	indexed []index.IndexedProject
}

// baseRel is one memoized baseTrunkRelation answer: trunk ancestry plus
// landings-behind-tip for a change's base at one trunk position.
type baseRel struct {
	onTrunk bool
	behind  int32
}

// effectiveGlobalChecks is the org-wide required-check set the §13.5 gate
// enforces: daemon flag config unioned with the org's stored settings. A
// directory read failing must not silently drop policy - but flag-level
// checks still apply, and the stored half is retried on the next request.
func (s *Server) effectiveGlobalChecks(ctx context.Context) []string {
	names := s.GlobalRequiredChecks
	if s.SettingsOrg != "" && s.Directory != nil {
		if settings, err := s.Directory.GetOrgSettings(ctx, s.SettingsOrg); err == nil {
			names = mergeCheckNames(names, settings.GlobalRequiredChecks)
		} else {
			log.Printf("runkod: org %q settings unavailable for merge gate (flag-level checks still apply): %v", s.SettingsOrg, err)
		}
	}
	return names
}

// effectiveRevalidationScope resolves the §13.5 revalidation tier: org
// setting revalidation_policy > daemon --revalidation > conflict-only (the
// 2026-07-15 default). Live per request like effectiveGlobalChecks.
func (s *Server) effectiveRevalidationScope(ctx context.Context) land.RevalidationScope {
	var dir Directory
	if s.SettingsOrg != "" {
		dir = s.Directory
	}
	return resolveRevalidation(ctx, dir, s.SettingsOrg, s.Revalidation)
}

// resolveRevalidation is the shared tier resolution the Server and the
// receive-funnel Processor both use, so the land gate and the §13.5
// carry-forward can never disagree about the effective policy.
func resolveRevalidation(ctx context.Context, dir Directory, orgName string, flagScope land.RevalidationScope) land.RevalidationScope {
	if dir != nil && orgName != "" {
		if settings, err := dir.GetOrgSettings(ctx, orgName); err == nil && settings.RevalidationPolicy != "" {
			switch scope := land.RevalidationScope(settings.RevalidationPolicy); scope {
			case land.RevalidationConflictOnly, land.RevalidationAffectedIntersection, land.RevalidationAlways:
				return scope
			default:
				// The write path validates, so a bad stored value means
				// manual meddling - fall through to the flag, loudly.
				log.Printf("runkod: org %q stored revalidation_policy %q is not a tier - using the daemon default", orgName, settings.RevalidationPolicy)
			}
		}
	}
	if flagScope == "" {
		return land.RevalidationConflictOnly
	}
	return flagScope
}

func (s *Server) clock() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// repoMount is the git mount this server ADVERTISES (org info, the
// workspace API's RepoPath): the org-named form on org-scoped servers -
// `git clone` names the checkout folder after the URL's last segment, and
// "repo" (every org repo's on-disk basename) says nothing about which org
// you just cloned. The root-mounted default server keeps its repo-dir
// basename (which IS the default org's name by construction, main.go).
func (s *Server) repoMount() string {
	if s.OrgName != "" {
		return s.OrgName + ".git"
	}
	return RepoMountName(s.RepoDir)
}

// Handler assembles the full mux: smart-HTTP git hosting at
// /<RepoMountName>/ (plus the advertised org-named alias on org servers),
// the internal pre-receive callback, and the bearer-token-authed REST API.
func (s *Server) Handler() (http.Handler, error) {
	mux := http.NewServeMux()

	gitHandler, err := GitHTTPHandler(s.RepoDir)
	if err != nil {
		return nil, fmt.Errorf("runkod: build git smart-HTTP handler: %w", err)
	}
	mount := RepoMountName(s.RepoDir)
	authedGit := s.requireGitAuth(gitHandler)
	mux.Handle("/"+mount+"/", authedGit)
	// Org-named alias (see repoMount): the historical /repo.git/ mount
	// above stays served forever - every existing remote and CI config
	// keeps working - the alias is only ADDITIONALLY routed.
	if alias := s.repoMount(); alias != mount {
		mux.Handle("/"+alias+"/", rewriteGitMount(alias, mount, authedGit))
	}

	// Unauthenticated by design: liveness/readiness probes and metrics
	// scrapers (compose healthcheck, k8s, Prometheus) cannot carry the
	// deploy token, and none of these leak repository content (§9.4's
	// stage-14 conventions: /healthz + /readyz + /metrics). The two
	// probes carry public CORS: the admin panel (webadmin/) renders
	// control-plane health from its own origin in the dev loop, and a
	// wildcard origin on an unauthenticated liveness bit gives nothing
	// away.
	mux.HandleFunc("/healthz", publicCORS(http.MethodGet, s.handleHealthz))
	mux.HandleFunc("/readyz", publicCORS(http.MethodGet, s.handleReadyz))
	mux.HandleFunc("GET /metrics", s.handleMetrics)

	mux.HandleFunc("POST /internal/pre-receive", s.handlePreReceive)

	mux.HandleFunc("GET /api/mirror/status", s.requireAuth(s.handleMirrorStatus))
	mux.HandleFunc("POST /api/mirror/unfreeze", s.requireAuth(s.handleMirrorUnfreeze))
	mux.HandleFunc("POST /api/github/connect", s.requireAuth(s.handleGithubConnect))
	mux.HandleFunc("GET /api/changes", s.requireReadAuth(s.handleListChanges))
	mux.HandleFunc("GET /api/changes/{key}", s.requireReadAuth(s.handleGetChange))
	mux.HandleFunc("POST /api/changes/{key}/abandon", s.requireAuth(s.handleAbandonChange))
	mux.HandleFunc("POST /api/changes/{key}/describe", s.requireAuth(s.handleDescribeChange))
	mux.HandleFunc("POST /api/changes/{key}/sync", s.requireAuth(s.handleSyncChange))
	mux.HandleFunc("POST /api/changes/{key}/checks/{name}/rerun", s.requireAuth(s.handleRerunCheck))
	mux.HandleFunc("GET /api/changes/{key}/affected", s.requireReadAuth(s.handleGetAffected))
	mux.HandleFunc("GET /api/changes/{key}/merge-requirements", s.requireReadAuth(s.handleGetMergeRequirements))
	mux.HandleFunc("POST /api/changes/{key}/checks", s.requireAuth(s.handlePostCheck))
	mux.HandleFunc("POST /api/deploys/{sha}/images", s.requireAuth(s.handlePostDeployImage))
	mux.HandleFunc("POST /api/changes/{key}/approve", s.requireAuth(s.handleApproveChange))
	mux.HandleFunc("POST /api/changes/{key}/automerge", s.requireAuth(s.handleSetAutomerge))
	mux.HandleFunc("POST /api/changes/{key}/land", s.requireAuth(s.handleLandChange))
	mux.HandleFunc("POST /api/changes/{key}/land-stack", s.requireAuth(s.handleLandStack))
	mux.HandleFunc("GET /api/changes/{key}/comments", s.requireReadAuth(s.handleListComments))
	mux.HandleFunc("POST /api/changes/{key}/comments", s.requireAuth(s.handleCreateComment))
	mux.HandleFunc("POST /api/changes/{key}/comments/{id}/resolve", s.requireAuth(s.handleResolveComment))
	mux.HandleFunc("POST /api/changes/{key}/request-review", s.requireAuth(s.handleRequestReview))
	mux.HandleFunc("GET /api/changes/{key}/review-requests", s.requireReadAuth(s.handleListReviewRequests))
	mux.HandleFunc("GET /api/search", s.requireReadAuth(s.handleSearch))

	mux.HandleFunc("GET /api/projects", s.requireReadAuth(s.handleListProjects))
	// Project deletion - create's dual (§13.1): the CLI's server-calling
	// verb; the same core the Connect surface uses (deleteproject.go).
	mux.HandleFunc("POST /api/projects/{name}/delete", s.requireAuth(s.handleDeleteProject))
	// Governance bootstrap for an ownerless org (§6.10 retrofit) - opens
	// the self-landable root-OWNERS change (bootstraporg.go).
	mux.HandleFunc("POST /api/org/bootstrap", s.requireAuth(s.handleBootstrapOrg))
	mux.HandleFunc("POST /api/projects/{name}/releases", s.requireAuth(s.handleCreateRelease))
	mux.HandleFunc("GET /api/projects/{name}/releases", s.requireReadAuth(s.handleListReleases))
	mux.HandleFunc("GET /api/affected", s.requireReadAuth(s.handleAffectedByPaths))

	mux.HandleFunc("POST /api/workspaces", s.requireAuth(s.handleCreateWorkspace))
	mux.HandleFunc("GET /api/workspaces", s.requireAuth(s.handleListWorkspaces))
	mux.HandleFunc("GET /api/workspaces/{id}", s.requireAuth(s.handleGetWorkspace))
	mux.HandleFunc("POST /api/workspaces/{id}/base", s.requireAuth(s.handleUpdateWorkspaceBase))
	mux.HandleFunc("DELETE /api/workspaces/{id}", s.requireAuth(s.handleDeleteWorkspace))
	mux.HandleFunc("POST /api/workspaces/{id}/activity", s.requireAuth(s.handleRecordWorkspaceActivity))

	// Ephemeral agent identity (agentprincipal.go): mint/list/revoke.
	mux.HandleFunc("POST /api/agents", s.requireAuth(s.handleMintAgentPrincipal))
	mux.HandleFunc("GET /api/agents", s.requireAuth(s.handleListAgentPrincipals))
	mux.HandleFunc("POST /api/agents/{name}/revoke", s.requireAuth(s.handleRevokeAgentPrincipal))
	mux.HandleFunc("GET /api/sparse-patterns", s.requireAuth(s.handleSparsePatterns))

	// The Connect RPC surface for the web frontend (proto/runko/v1, §17.4;
	// rpc.go) - same Store, same cores, same token, one more transport.
	s.mountRPC(mux)

	// whoami validates a credential and names the caller - the web UI's
	// sign-in check (auth.go). Mounted method-less behind rpcMiddleware so
	// the browser's CORS preflight (OPTIONS, unauthenticated) works from a
	// dev-server origin exactly like the RPC routes.
	mux.Handle("/api/whoami", s.rpcMiddleware(http.HandlerFunc(s.handleWhoami)))

	// Sign-up + its discovery config are unauthenticated by design - see
	// signup.go - and carry public CORS headers: the deployed layout is
	// same-origin, but the dev loop (Vite on one port, daemon on another)
	// is not, and the login page must be able to ask "is signup on?"
	// before anyone has a credential. Found by driving the real UI: the
	// first cut had no CORS here and the sign-up offer silently never
	// appeared cross-origin.
	mux.HandleFunc("/api/signup", publicCORS(http.MethodPost, s.handleSignup))
	mux.HandleFunc("/api/auth/config", publicCORS(http.MethodGet, s.handleAuthConfig))

	// Invite requests (§15.1; invite.go): the public intake shares
	// signup's CORS posture (the login gate posts it pre-credential); the
	// drain feed + acks are the mailer service's surface, operator-only,
	// server-to-server (no preflight, so method-qualified patterns).
	mux.HandleFunc("/api/invite-requests", publicCORS(http.MethodPost, s.handleCreateInviteRequest))
	// The landing page's contact form: the same intake with kind=contact
	// (invite.go), bound for the same operator mailbox.
	mux.HandleFunc("/api/contact", publicCORS(http.MethodPost, s.handleCreateContactMessage))
	// The mailer drain surface moved to Connect (InviteFeedService,
	// runkod/proto/mailer/v1 - §13.3.1's first in-boundary contract; the
	// operator gate lives in invitefeed.go's requireOperatorRPC). Only the
	// public intake above stays REST.

	return mux, nil
}

// handleWhoami reports the authenticated caller's identity: a named
// principal ({name, is_agent}), a bot lane ({name, lane}), or the
// anonymous deploy token ({anonymous}). rpcMiddleware already rejected
// invalid credentials with 401.
func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c := s.callerForAuthHeader(r.Header.Get("Authorization"))
	switch {
	case c.principal != nil:
		writeJSON(w, http.StatusOK, map[string]any{
			"name": c.principal.Name, "is_agent": c.principal.IsAgent, "anonymous": false,
			// operator: flag-configured (server config), not a signup row -
			// the deployment admin surface keys on this (orghub.go).
			"operator": !c.principal.Stored && !c.principal.IsAgent, "admin": c.principal.Admin,
		})
	case c.lane != nil:
		writeJSON(w, http.StatusOK, map[string]any{
			"name": c.lane.Name, "lane": true, "anonymous": false, "operator": false,
		})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"name": "", "anonymous": true, "operator": true})
	}
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

// handleHealthz is the ops floor (§28.3 stage 12c-④): 200 when the daemon
// is up and its bare repo is where it expects, 503 otherwise. Liveness
// only - it deliberately does NOT round-trip Postgres or git subprocesses,
// so a probe can run every few seconds without load.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if _, err := os.Stat(s.RepoDir); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable", "reason": fmt.Sprintf("repo dir: %v", err),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz is /healthz plus the dependency probe (§9.4): ready means
// "this replica can serve real traffic NOW" - repo present AND the Store's
// backing service reachable (a real Postgres round-trip in the durable
// profile). Liveness and readiness stay separate endpoints so an
// orchestrator can restart a dead process without draining a replica
// that's merely waiting on its database.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if _, err := os.Stat(s.RepoDir); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable", "reason": fmt.Sprintf("repo dir: %v", err),
		})
		return
	}
	if err := s.Store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable", "reason": fmt.Sprintf("store: %v", err),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMetrics is a minimal Prometheus text-format exposition (§9.4's
// stage-14 convention) - hand-rolled because the exposition format for
// gauges is trivially a few text lines, and importing client_golang for
// that would violate the lean-dependency posture (§28.2; the Zoekt
// precedent). Grows real counters when something needs them; until then
// it answers the two questions an operator actually asks a fresh eval
// stack: is it up (uptime), and is work flowing (open changes).
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	open, err := s.Store.ListChanges(r.Context(), "open")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP runkod_up Whether the daemon is serving.\n# TYPE runkod_up gauge\nrunkod_up 1\n")
	fmt.Fprintf(w, "# HELP runkod_uptime_seconds Seconds since this process started serving.\n# TYPE runkod_uptime_seconds gauge\nrunkod_uptime_seconds %d\n", int64(time.Since(processStart).Seconds()))
	fmt.Fprintf(w, "# HELP runkod_open_changes Open Changes in the store.\n# TYPE runkod_open_changes gauge\nrunkod_open_changes %d\n", len(open))
	if s.Mirror != nil {
		fmt.Fprintf(w, "# HELP runkod_mirror_frozen Mirror refs frozen on divergence (unfreeze via POST /api/mirror/unfreeze).\n# TYPE runkod_mirror_frozen gauge\nrunkod_mirror_frozen %d\n", s.mirrorFrozenCount(r.Context()))
	}
}

var processStart = time.Now()

// requireAuth wraps a handler with deploy-token bearer auth. The
// /internal/pre-receive callback checks the SAME token itself (it isn't
// wrapped here, since it uses the shared secret as authentication between
// the daemon and its own installed hook, not a client-facing API).
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := s.callerForAuthHeader(r.Header.Get("Authorization"))
		if c.deniedOrg {
			writeAPIError(w, orgDeniedErr(s.OrgName))
			return
		}
		if !c.ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// No REST body is legitimately large (approve/check/workspace
		// JSON); cap it so a stuck client can't buffer unbounded memory.
		// The git smart-HTTP transport is deliberately NOT capped -
		// packfiles are legitimately huge; their limits live in the
		// receive funnel (snapshot size cap, §12.2).
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next(w, r)
	}
}

// publicReadEnabled reports whether this org opted in to anonymous read
// access (§15.2 public_read). Live per request like effectiveGlobalChecks;
// no settings org, no directory, or a directory error all fail CLOSED
// (private) - the opposite bias from effectiveGlobalChecks, where flag-level
// checks still applying is the safe direction.
func (s *Server) publicReadEnabled(ctx context.Context) bool {
	if s.SettingsOrg == "" || s.Directory == nil {
		return false
	}
	settings, err := s.Directory.GetOrgSettings(ctx, s.SettingsOrg)
	if err != nil {
		return false
	}
	return settings.PublicRead
}

// requireReadAuth is requireAuth for the §15.2 public-read allowlist: on a
// public_read org, a request presenting NO credentials at all passes as the
// anonymous read-only caller (every allowlisted route is method-qualified
// GET, so anonymity never reaches a write handler). Presented-but-wrong
// credentials still fail exactly as before - a typo'd token must surface as
// 401/403, never silently downgrade to the anonymous view.
func (s *Server) requireReadAuth(next http.HandlerFunc) http.HandlerFunc {
	authed := s.requireAuth(next)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" && s.publicReadEnabled(r.Context()) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			}
			next(w, r)
			return
		}
		authed(w, r)
	}
}

// isUploadPackRequest reports whether r is a smart-HTTP READ (clone/fetch):
// the ref advertisement for upload-pack, or the upload-pack POST itself.
// Everything else on the git mount - receive-pack in both phases, dumb-
// protocol paths - stays authenticated even on a public_read org.
func isUploadPackRequest(r *http.Request) bool {
	if strings.HasSuffix(r.URL.Path, "/git-upload-pack") && r.Method == http.MethodPost {
		return true
	}
	return strings.HasSuffix(r.URL.Path, "/info/refs") &&
		r.Method == http.MethodGet &&
		r.URL.Query().Get("service") == "git-upload-pack"
}

// tokenMatches reports whether the Authorization header carries ANY valid
// credential - bearer (deploy token, bot lane, principal) or Basic
// (principal name+password, or the deploy token as password). Identity
// resolution for attribution/gating lives on the same resolver
// (callerForAuthHeader, auth.go), so authentication and identity can never
// disagree.
func (s *Server) tokenMatches(authHeader string) bool {
	return s.callerForAuthHeader(authHeader).ok
}

func constantTimeEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// requireGitAuth gates the smart-HTTP git transport itself (§14.11,
// doc.go's scope boundary) - without this, anyone with network access
// could clone/push regardless of what the pre-receive hook enforces, since
// hooks only govern policy, not who may connect at all. Git clients
// authenticate via plain HTTP Basic (`git clone
// http://<any-user>:<token>@host/<repo>.git`), which every git client
// supports natively - no custom credential helper needed.
//
// When the token belongs to a named principal (§15.1's interim registry),
// the request is served by a per-request copy of the CGI handler with
// REMOTE_USER=<name> in its environment - git's own convention for
// authenticated receive. http-backend, git-receive-pack, and the
// pre-receive hook inherit it as ordinary process environment, and the
// hook forwards it back to the daemon alongside the quarantine vars, so
// the funnel knows WHO pushed without any signature change on the wire.
func (s *Server) requireGitAuth(git *cgi.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			// §15.2 public_read: anonymous clone/fetch on an opted-in org.
			// upload-pack ONLY - a credential-less receive-pack attempt
			// still 401s so git prompts for credentials. Workspace
			// snapshot refs (people's uncommitted WIP) and the rotating
			// refs/for tip are hidden from the anonymous advertisement;
			// refs/changes/* stays public by design. The config is
			// injected per-request via GIT_CONFIG_* env, so authenticated
			// callers (workspace attach fetching its snapshot ref) are
			// untouched.
			if s.publicReadEnabled(r.Context()) && isUploadPackRequest(r) {
				anon := *git // shallow copy; Env must not mutate the shared handler
				anon.Env = append(append([]string{}, git.Env...),
					"GIT_CONFIG_COUNT=2",
					"GIT_CONFIG_KEY_0=uploadpack.hideRefs", "GIT_CONFIG_VALUE_0=refs/workspaces",
					"GIT_CONFIG_KEY_1=uploadpack.hideRefs", "GIT_CONFIG_VALUE_1=refs/for",
				)
				anon.ServeHTTP(w, r)
				return
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="runko"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Proper name+password pairs first (auth.go), then the historical
		// password-only principal resolution - existing remotes embed
		// arbitrary usernames (`http://user:<token>@...`), and the token
		// alone is the secret, so a URL-borne username never blocks a
		// clone; it just doesn't claim someone else's identity.
		c := s.callerForBasic(user, pass)
		if !c.ok && !c.deniedOrg {
			if p := s.principalForBasicAuth(pass); p != nil {
				c = caller{ok: true, principal: p}
			}
		}
		if c.deniedOrg {
			// Valid account, wrong org (auth.go): a 403 git surfaces
			// verbatim, where a 401 would send the user chasing a
			// password that is not the problem.
			http.Error(w, fmt.Sprintf("forbidden: your account is not a member of org %q", s.OrgName), http.StatusForbidden)
			return
		}
		if !c.ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="runko"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if c.principal != nil {
			authed := *git // shallow copy; Env must not mutate the shared handler
			authed.Env = append(append([]string{}, git.Env...), "REMOTE_USER="+c.principal.Name)
			authed.ServeHTTP(w, r)
			return
		}
		if c.lane != nil {
			// Lane identity travels beside (never as) REMOTE_USER: lanes
			// are not principals, and overloading REMOTE_USER would subject
			// them to workspace owner checks and authored_by attribution
			// built for humans. Consumed only by the §14.10.3 tags gate.
			authed := *git // shallow copy; Env must not mutate the shared handler
			authed.Env = append(append([]string{}, git.Env...), "REMOTE_LANE="+c.lane.Name)
			authed.ServeHTTP(w, r)
			return
		}
		git.ServeHTTP(w, r)
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
	// headerRemoteUser carries the authenticated pusher's principal name
	// (§15.1 interim registry): requireGitAuth set REMOTE_USER on the CGI
	// env, the hook inherited it and forwards it here.
	headerRemoteUser = "X-Runko-Remote-User"
	// headerRemoteLane is headerRemoteUser's bot-lane sibling (§14.10.3,
	// stage 17): the lane name a tag push authenticated as, consumed only
	// by the funnel's tags gate.
	headerRemoteLane = "X-Runko-Remote-Lane"
	// headerPushOption carries the push's `git push -o` options (one header
	// value per option, in order) - receive-pack exposes them to the hook
	// as GIT_PUSH_OPTION_COUNT/GIT_PUSH_OPTION_<n>, which the daemon
	// reconstructs so the funnel sees exactly the env a local hook would
	// (§12.2 provenance: `runko change push` sends workspace=<id> /
	// workspace-branch=<name>).
	headerPushOption = "X-Runko-Push-Option"
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
	if v := r.Header.Get(headerRemoteUser); v != "" {
		extraEnv = append(extraEnv, "REMOTE_USER="+v)
	}
	if v := r.Header.Get(headerRemoteLane); v != "" {
		extraEnv = append(extraEnv, "REMOTE_LANE="+v)
	}
	if opts := r.Header.Values(headerPushOption); len(opts) > 0 {
		extraEnv = append(extraEnv, fmt.Sprintf("GIT_PUSH_OPTION_COUNT=%d", len(opts)))
		for i, opt := range opts {
			extraEnv = append(extraEnv, fmt.Sprintf("GIT_PUSH_OPTION_%d=%s", i, opt))
		}
	}

	results := s.Processor.ProcessBatch(r.Context(), updates, extraEnv)
	writeJSON(w, http.StatusOK, results)
}

// handleListChanges serves GET /api/changes?state=[&limit=&offset=] (§28.3
// stage 12c-③, the UI's first page): Changes in a state, newest first, all
// states when ?state is absent. limit/offset page at the store (stage 15;
// same offset semantics as the RPC's page_token); omitted, the historical
// full listing is unchanged.
func (s *Server) handleListChanges(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	switch state {
	case "", "open", "landed", "abandoned":
	default:
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_state", Field: "state",
			Message:    fmt.Sprintf("%q is not a change state", state),
			Suggestion: "use state=open|landed|abandoned, or omit it for all",
		})
		return
	}
	limit, ok := queryInt(w, r, "limit")
	if !ok {
		return
	}
	offset, ok := queryInt(w, r, "offset")
	if !ok {
		return
	}
	var list []Change
	var err error
	if limit > 0 || offset > 0 {
		list, err = s.Store.ListChangesPage(r.Context(), state, limit, offset)
	} else {
		list, err = s.Store.ListChanges(r.Context(), state)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []Change{}
	}
	writeJSON(w, http.StatusOK, list)
}

// queryInt parses an optional non-negative integer query parameter, writing
// a structured 400 (and returning ok=false) on garbage - a silent fallback
// to "no limit" would hand a typo the full history.
func queryInt(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_" + name, Field: name,
			Message:    fmt.Sprintf("%q is not a non-negative integer", raw),
			Suggestion: "pass a non-negative integer, or omit it",
		})
		return 0, false
	}
	return v, true
}

// handleAbandonChange serves POST /api/changes/{key}/abandon (§7.4's third
// state, settable for the first time in stage 12c-③). No webhook: the
// envelope schema's event enum has no abandoned event (docs/spec/webhooks),
// and the schema is the contract - widening it is a spec change, not a side
// effect of this endpoint.
func (s *Server) handleAbandonChange(w http.ResponseWriter, r *http.Request) {
	change, apiErr := s.abandonChangeCore(r.Context(), r.PathValue("key"), s.principalFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, change)
}

// handleSyncChange serves POST /api/changes/{key}/sync: server-side stack
// rebase onto the current trunk tip (see sync.go). The non-synced outcomes
// (already in sync, conflict) are response fields, not errors - the same
// stance as land's outcome modeling.
func (s *Server) handleSyncChange(w http.ResponseWriter, r *http.Request) {
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
	dec, apiErr := s.syncChangeCore(r.Context(), key, change, s.principalFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"synced":             dec.Synced,
		"already_in_sync":    dec.AlreadyInSync,
		"conflict_change_id": dec.ConflictChange,
		"conflicts":          dec.ConflictPaths,
	})
}

// handleRerunCheck serves POST /api/changes/{key}/checks/{name}/rerun -
// §14.4.2's re-run flow, wired to the wire for the first time (stage
// 12c-③; checks.RerunCheck and the change.check_rerun_requested webhook
// schema existed since stage 8 with no caller). The daemon never runs CI
// (§14): rerunning means resetting the run to queued and emitting the
// webhook the org's CI plugin maps to a provider-specific re-run. Responds
// with the refreshed merge requirements, the same shape approve returns.
func (s *Server) handleRerunCheck(w http.ResponseWriter, r *http.Request) {
	key, name := r.PathValue("key"), r.PathValue("name")
	change, ok, err := s.Store.GetChange(r.Context(), key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "change not found", http.StatusNotFound)
		return
	}
	reqs, apiErr := s.rerunCheckCore(r.Context(), key, change, name, s.principalFor(r), s.laneFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, reqs)
}

// enqueueRerunWebhook emits change.check_rerun_requested (§14.4.2): the
// org's CI plugin maps the rerun block to a provider-specific re-run.
func (s *Server) enqueueRerunWebhook(ctx context.Context, change Change, checkName, requestedBy string) {
	actor := checks.WebhookActor{Type: "user", ID: requestedBy}
	if requestedBy == "" {
		actor.ID = "unknown"
	}
	env := checks.WebhookEnvelope{
		SpecVersion: "1",
		DeliveryID:  change.ChangeKey + "@rerun@" + checkName + "@" + s.clock().UTC().Format(time.RFC3339),
		Type:        "change.check_rerun_requested",
		OccurredAt:  s.clock(),
		OrgID:       s.SettingsOrg,
		Change: checks.WebhookChange{
			ID: change.ChangeKey, State: change.State,
			BaseSHA: change.BaseSHA, HeadSHA: change.HeadSHA, GitRef: change.GitRef,
			Title: change.Title, Actor: actor,
		},
		Rerun: &checks.WebhookRerun{CheckName: checkName, RequestedBy: actor},
	}
	// The rerun envelope must carry the same affected block change.updated
	// carries (migration-findings #31): CI plugins scope conditional jobs
	// on affected_projects (e.g. runko-checks.yml's web-check job), so a
	// rerun without it silently SKIPS those jobs - the run comes back
	// green while the check stays pending forever. Degrade to no block on
	// computation error (logged): that is exactly the pre-fix behavior,
	// never worse.
	if result, _, err := s.computeAffected(change); err != nil {
		log.Printf("runkod: %s: affected for rerun webhook: %v", change.ChangeKey, err)
	} else {
		env.Affected = &checks.WebhookAffected{
			ComputationID: result.ComputationID,
			Paths:         result.Paths,
			ReasonCodes:   result.ReasonCodes,
			RunEverything: result.RunEverything,
		}
		for _, pr := range result.Projects {
			env.Affected.Projects = append(env.Affected.Projects, checks.WebhookAffectedProject{Name: pr.Name, Path: pr.Path})
		}
	}
	payload, err := checks.MarshalEnvelope(env)
	if err != nil {
		log.Printf("runkod: %s: marshal rerun webhook: %v", change.ChangeKey, err)
		return
	}
	if _, err := s.Store.EnqueueWebhook(ctx, env.Type, payload); err != nil {
		log.Printf("runkod: %s: enqueue rerun webhook: %v", change.ChangeKey, err)
	}
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

	result, _, err := s.computeAffected(change)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// computeAffected is GET .../affected's computation, factored out so
// mergeRequirements can derive required check names from the SAME affected
// project set a client would see from that endpoint - one computation, not
// two that could quietly drift. Scans at change.HeadSHA (the Change's own
// tree), matching what `runko-ci affected` itself would have computed
// against this Change's base/head when CI posted its checks - not trunk's
// current tip, which is attemptLand's (land.go) concern, not this one.
func (s *Server) computeAffected(change Change) (affected.Result, []index.IndexedProject, error) {
	cacheKey := change.BaseSHA + "\x00" + change.HeadSHA
	s.affectedMu.Lock()
	if e, ok := s.affectedCache[cacheKey]; ok {
		s.affectedMu.Unlock()
		return e.result, e.indexed, nil
	}
	s.affectedMu.Unlock()

	store := gitstore.New(s.RepoDir)
	indexed, err := s.indexedProjectsAt(store, core.Revision(change.HeadSHA))
	if err != nil {
		return affected.Result{}, nil, fmt.Errorf("scan projects: %w", err)
	}
	projects := index.AffectedProjectInfos(indexed)

	base := change.BaseSHA
	if base == "" {
		base = emptyTreeOID
	}
	changedPaths, err := gitDiffNamesOnly(s.RepoDir, base, change.HeadSHA)
	if err != nil {
		return affected.Result{}, nil, fmt.Errorf("diff: %w", err)
	}

	// Tree-declared patterns first (§9.4: the tree owns policy), daemon
	// flags as an additive override.
	rootInvalidation := index.RootInvalidation(indexed)
	if s.Processor != nil {
		rootInvalidation = append(rootInvalidation, s.Processor.RootInvalidationPatterns...)
	}
	result := affected.Compute(projects, changedPaths, affected.Options{
		RootInvalidationPatterns: rootInvalidation,
		ProsePatterns:            index.Prose(indexed),
	})

	s.affectedMu.Lock()
	if s.affectedCache == nil {
		s.affectedCache = map[string]affectedEntry{}
	}
	// SHA-keyed entries never expire, but the map must not grow with every
	// amend ever pushed; past ~512 live (base, head) pairs just start over.
	if len(s.affectedCache) >= 512 {
		s.affectedCache = map[string]affectedEntry{}
	}
	s.affectedCache[cacheKey] = affectedEntry{result: result, indexed: indexed}
	s.affectedMu.Unlock()
	return result, indexed, nil
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
	req, err := s.mergeRequirements(r.Context(), key, change, s.laneFor(r))
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
// The invariant is per-principal: lane is the bot lane the CALLER
// authenticated as (nil for the deploy token), and both handlers resolve it
// from the same request auth, so any given client always sees the gate it
// will actually be held to.
//
// For a bot lane (§14.10.2), the human owner-approval requirement is waived
// - the lane's entire purpose - and the lane's own RequiredChecks are added
// on top of what the tree requires. The lane's path-allowlist constraint is
// enforced separately by handleLandChange (it refuses before gating).
//
// Default-deny (§28.3 stage 11c): if NO policy resolves for a non-lane
// caller - zero required checks (ci.checks + org globals) and zero owner
// requirements - the Change is not mergeable unless AllowUnpolicedLand is
// set. A lane caller is exempt: the lane grant itself is resolvable policy
// (explicit org config with its own mandatory check set).
func (s *Server) mergeRequirements(ctx context.Context, key string, change Change, lane *BotLane) (checks.MergeRequirements, error) {
	runs, err := s.Store.ListCheckRuns(ctx, key, change.HeadSHA)
	if err != nil {
		return checks.MergeRequirements{}, err
	}
	result, indexed, err := s.computeAffected(change)
	if err != nil {
		return checks.MergeRequirements{}, err
	}
	requiredNames := requiredCheckNames(result, indexed)
	requiredNames = mergeCheckNames(requiredNames, s.effectiveGlobalChecks(ctx))

	var owners []checks.OwnerRequirement
	if lane != nil {
		requiredNames = mergeCheckNames(requiredNames, lane.RequiredChecks)
	} else {
		owners, err = s.ownerRequirements(ctx, key, change.HeadSHA, change.AuthoredBy, result, indexed)
		if err != nil {
			return checks.MergeRequirements{}, err
		}
	}

	// §14.4.2 staleness, consulted for the first time in stage 12c-③: a
	// REQUIRED run stuck in queued/in_progress past its TTL gets a loud
	// blocker naming it ("a dead CI must block loudly, not hang silently")
	// - it was already non-mergeable by being pending; the blocker tells
	// the human WHY nothing is progressing and rerun is the way out.
	now := s.clock()
	requiredSet := make(map[string]bool, len(requiredNames))
	for _, n := range requiredNames {
		requiredSet[n] = true
	}
	var staleNames []string
	for _, run := range runs {
		if requiredSet[run.Name] && !run.LastSeenAt.IsZero() &&
			checks.IsStale(run.Status, run.LastSeenAt, run.TTLSeconds, now) {
			staleNames = append(staleNames, run.Name)
		}
	}
	sort.Strings(staleNames)

	req := checks.ComputeMergeRequirements(key, owners, requiredNames, runs, nil, staleNames, nil)

	// A change whose base is not on trunk cannot land regardless of gate
	// state - attemptLand refuses it (§7.4's ancestors-land-first rule).
	// Saying "mergeable" while land 409s was a lie the UI faithfully
	// repeated (found live: abandon a stack's bottom and the pending
	// child kept its green chip). Name the parent when we know it.
	if !s.baseOnTrunk(change.BaseSHA) && !s.parentLandedOnTrunk(ctx, change.BaseSHA) {
		req.Mergeable = false
		if parent, ok := s.changeWithHead(ctx, change.BaseSHA); ok {
			verb := "land it first"
			switch parent.State {
			case "abandoned":
				verb = "reopen it (re-push its stack) or rebase this change onto trunk and re-push"
			case "landed":
				// Landed but its landed commit is NOT on trunk anymore
				// (history rewound) - genuinely stranded. A landed parent
				// whose commit IS on trunk no longer reaches here: the
				// child is mergeable and rebases at land time (§13.5).
				verb = "its landed commit is no longer on trunk - rebase this change onto trunk and re-push"
			}
			req.Blockers = append(req.Blockers, fmt.Sprintf("stacked on %s (%q, %s) - %s", parent.ChangeKey, firstLine(parent.Title), parent.State, verb))
		} else {
			req.Blockers = append(req.Blockers, fmt.Sprintf("stacked on a commit trunk does not have (base %.12s) - rebase onto trunk and re-push", change.BaseSHA))
		}
	}

	if lane == nil && !s.AllowUnpolicedLand && len(req.RequiredChecks) == 0 && len(req.RequiredOwners) == 0 {
		req.Mergeable = false
		req.Blockers = append(req.Blockers,
			"no merge policy resolves for this change: its touched paths require no checks (no ci.checks, no org global checks) and no owner approvals - landing unpoliced changes is refused outside the eval profile (an ownerless org seeds its governance with `runko org bootstrap`; otherwise declare owners/ci.checks in PROJECT.yaml, or start runkod with --insecure-allow-unpoliced-land)")
	}

	// Agent changes must carry a description before they land (§8.7 gate on
	// §8.6 state): the §8.6 blurb is control-plane prose an agent sets with
	// `runko change describe` - never derived from the commit message - and
	// RequireDescription (default on) turns its ABSENCE into a merge blocker
	// for agent-authored changes, so an agent cannot land work no reviewer
	// can read without the diff. Humans and the anonymous deploy token are
	// exempt (an AgentPolicy gate); a bot lane lands under its own policy, not
	// this one. Same runkod-side post-aggregation seam as the overrides above.
	if lane == nil {
		if policy, isAgent := s.agentPolicyForAuthor(ctx, change.AuthoredBy); isAgent && policy.RequireDescription && strings.TrimSpace(change.Description) == "" {
			req.Mergeable = false
			req.Blockers = append(req.Blockers,
				"this change has no description - agent changes must summarize WHAT changed and WHY before landing (§8.7); add one with `runko change describe --description \"...\"`")
		}
	}

	// Review conversation (§13.4.1-13.4.2): the unresolved-threads blocker
	// (org opt-in, default off) and the derived attention set - same
	// runkod-side post-aggregation seam as the stacked-base and default-deny
	// overrides above. Bot lanes skip both: a lane's gate is its own check
	// set, and "whose turn is it" has no meaning for an automated lander.
	if lane == nil {
		comments, err := s.Store.ListComments(ctx, key, 0, 0)
		if err != nil {
			return checks.MergeRequirements{}, err
		}
		if s.requireResolvedThreads(ctx) {
			unresolved := 0
			for _, c := range comments {
				if c.ParentID == "" && !c.Resolved {
					unresolved++
				}
			}
			if unresolved > 0 {
				req.Mergeable = false
				req.Blockers = append(req.Blockers, fmt.Sprintf(
					"%d unresolved review thread(s) - resolve them (this org sets require_resolved_threads)", unresolved))
			}
		}
		requests, err := s.Store.ListReviewRequests(ctx, key)
		if err != nil {
			return checks.MergeRequirements{}, err
		}
		req.AttentionSet = attentionSet(change, owners, requests, comments)
	}
	return req, nil
}

// mergeCheckNames unions extra into names, preserving sorted order and
// dropping duplicates.
func mergeCheckNames(names, extra []string) []string {
	if len(extra) == 0 {
		return names
	}
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		seen[n] = true
	}
	out := append([]string{}, names...)
	for _, n := range extra {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// ownerRequirements derives §13.5's "required human owners approved" gate
// inputs (§28.3 stage 11c - owners were previously nil unconditionally, so
// the gate row was decorative at the wire level). The REQUIRED side comes
// from the tree, per §7.3's "touched paths in a Change compute required
// owners": each changed path maps to its owning project by longest prefix,
// and that project's resolved owners (manifest > OWNERS > org default, the
// stage-4 index) are required. Deliberately NOT the transitive affected
// closure - a dependent project's tests must run (requiredCheckNames scopes
// to the closure), but its owners didn't have code touched and get no
// approval veto. Projects with no owners anywhere contribute no requirement
// (§7.3 "gaps visible; optionally blocking" - the optional block is future
// 11c work, not a default). The SATISFIED side joins stored approvals; an
// approval for an owner the tree no longer requires is simply ignored,
// never resurrected as a requirement (tree-as-truth, §10.3).
//
// authoredBy is who pushed the change's current head. A HUMAN author who is
// themselves a required owner satisfies that requirement implicitly - the
// push is the consent the gate exists to collect (Gerrit's uploader model,
// §6.10). Without this, a path whose ONLY owner is the author can never
// land: the approve action denies self-approval (actions.go), so the
// requirement is unsatisfiable - the exact deadlock a fresh org would hit
// the moment genesis seeds OWNERS with its creator. Agents never
// self-satisfy: §8.7's "no approving at all" covers their own authorship,
// so an agent-authored change always keeps a human owner in the loop.
func (s *Server) ownerRequirements(ctx context.Context, key, headSHA, authoredBy string, result affected.Result, indexed []index.IndexedProject) ([]checks.OwnerRequirement, error) {
	required := map[string]bool{}
	for _, path := range result.Paths {
		project, ok := owningProject(indexed, path)
		if !ok {
			continue
		}
		for _, o := range project.Owners {
			required[o.Ref] = true
		}
	}
	if len(required) == 0 {
		return nil, nil
	}

	approvals, err := s.Store.ListApprovals(ctx, key)
	if err != nil {
		return nil, err
	}
	approved := map[string]bool{}
	for _, a := range approvals {
		// §13.5 (decided 2026-07-07): an approval satisfies the gate only
		// for the head it was granted for - an amend moves the head and
		// the requirement returns to outstanding, exactly as check runs
		// (keyed by head_sha) already invalidate. "" (a pre-stage-12c row)
		// never matches: unknown approval head reads as stale, fail closed.
		if a.HeadSHA == headSHA {
			approved[a.OwnerRef] = true
		}
	}
	if authoredBy != "" && required[authoredBy] {
		// Owner refs and principal names share a flat namespace for direct
		// user refs; group refs ("group:...") can never equal a principal
		// name, so this match is exact-user only - group membership never
		// self-satisfies.
		if _, isAgent := s.agentPolicyForAuthor(ctx, authoredBy); !isAgent {
			approved[authoredBy] = true
		}
	}

	refs := make([]string, 0, len(required))
	for ref := range required {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	out := make([]checks.OwnerRequirement, len(refs))
	for i, ref := range refs {
		out[i] = checks.OwnerRequirement{OwnerRef: ref, Satisfied: approved[ref]}
	}
	return out, nil
}

// owningProject returns the project owning path by longest-path-prefix match,
// the same rule affected.Compute and tagProjects apply (§13.3). A repo-root
// project (Path == "") matches everything at the lowest priority.
func owningProject(indexed []index.IndexedProject, path string) (index.IndexedProject, bool) {
	var best index.IndexedProject
	found := false
	for _, p := range indexed {
		matches := p.Path == "" || path == p.Path || strings.HasPrefix(path, p.Path+"/")
		if !matches {
			continue
		}
		if !found || len(p.Path) > len(best.Path) {
			best = p
			found = true
		}
	}
	return best, found
}

// requiredCheckNames derives what's ACTUALLY required for change to land:
// the union of each affected project's PROJECT.yaml ci.checks (§14.9),
// scoped by the same affected computation GET .../affected reports (or
// every indexed project when RunEverything is set - affected.Result's
// Projects list is an incomplete view by construction whenever that flag
// is true, §13.3, so scoping to it here would silently under-require).
//
// Found in review (§28.3 stage 11b's follow-up): this used to derive
// "required" from whatever check runs had already been POSTED
// (requiredNames := every run.Name) - self-referential policy where zero
// reported runs meant zero requirements, so a Change with no checks and no
// owners landed successfully. Declared ci.checks is the only required-check
// source actually modeled anywhere in this codebase today (no org-level
// global-checks table exists in db/migrations either) - wiring it through
// is a real, if partial, fix: a project with no ci block still requires
// nothing (anti-Boq, §6.2), but a project that DOES declare checks now
// actually gates on them, reported or not.
func requiredCheckNames(result affected.Result, indexed []index.IndexedProject) []string {
	type scopedProject struct {
		p      index.IndexedProject
		direct bool
	}
	var scoped []scopedProject
	if result.RunEverything {
		// Fail closed (§14.5.9): under run_everything every project counts
		// as direct, so both check classes gate.
		for _, p := range indexed {
			scoped = append(scoped, scopedProject{p: p, direct: true})
		}
	} else {
		byName := make(map[string]index.IndexedProject, len(indexed))
		for _, p := range indexed {
			byName[p.Name] = p
		}
		for _, ref := range result.Projects {
			if p, ok := byName[ref.Name]; ok {
				scoped = append(scoped, scopedProject{p: p, direct: ref.Direct})
			}
		}
	}

	seen := map[string]bool{}
	var names []string
	for _, sp := range scoped {
		// index.ChecksFor is the shared §14.5.9 rule - the executor
		// (runko-ci checks) resolves through the same function, so the
		// gate can never require a check the matrix won't run.
		for _, c := range index.ChecksFor(sp.p, sp.direct) {
			if !seen[c.Name] {
				seen[c.Name] = true
				names = append(names, c.Name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// approveRequest is POST /api/changes/{key}/approve's body. ApprovedBy is
// client-supplied identity - see the Approval type's trust-boundary note.
type approveRequest struct {
	OwnerRef   string `json:"owner_ref"`
	ApprovedBy string `json:"approved_by"`
}

// handleApproveChange records an owner approval (§13.5's "required human
// owners approved" gate, §28.3 stage 11c). The owner_ref must be one the
// tree currently requires for this Change's touched paths - approving a
// ref nothing requires is a client mistake surfaced as a structured 400,
// not silently recorded. Responds with the refreshed merge requirements so
// the approver immediately sees what their approval covered and what still
// blocks (§7.3's aggregation UX, minimally).
func (s *Server) handleApproveChange(w http.ResponseWriter, r *http.Request) {
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

	var req approveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_body", Message: "request body must be JSON with owner_ref and approved_by",
		})
		return
	}

	reqs, apiErr := s.approveChangeCore(r.Context(), key, change, req.OwnerRef, req.ApprovedBy, s.principalFor(r))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, reqs)
}

// handleListProjects serves the tree's project index at the current trunk
// tip (§10.3: the control plane is a rebuildable index; this endpoint scans
// live rather than caching, same stance as handleGetAffected). Added at
// §28.3 stage 12 as the REST substrate for the MCP adapter's list_projects/
// get_project/who_owns tools (§8.3: MCP tools are thin wrappers over the
// same REST handlers every other client uses). An unborn trunk is an empty
// list, not an error - orientation over an empty monorepo is empty.
func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	gstore := gitstore.New(s.RepoDir)
	trunkTip, err := gstore.ResolveRef("refs/heads/" + s.TrunkRef)
	if err != nil {
		writeJSON(w, http.StatusOK, []index.IndexedProject{})
		return
	}
	indexed, err := s.indexedProjectsAt(gstore, trunkTip)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if indexed == nil {
		indexed = []index.IndexedProject{}
	}
	writeJSON(w, http.StatusOK, indexed)
}

// handleAffectedByPaths computes affected projects for an arbitrary path
// set at the current trunk tip - get_affected's paths mode (§13.3), as
// opposed to handleGetAffected's change mode (which diffs a Change's own
// base..head). Same pure computation, same org root-invalidation config.
func (s *Server) handleAffectedByPaths(w http.ResponseWriter, r *http.Request) {
	paths := splitCommaList(r.URL.Query().Get("paths"))
	if len(paths) == 0 {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "paths",
			Message: "pass ?paths=<path>[,<path>...]",
		})
		return
	}
	gstore := gitstore.New(s.RepoDir)
	trunkTip, err := gstore.ResolveRef("refs/heads/" + s.TrunkRef)
	if err != nil {
		writeJSON(w, http.StatusConflict, clierr.Error{
			Code: "trunk_unborn", Field: "monorepo",
			Message: fmt.Sprintf("trunk %s has no commits yet", s.TrunkRef),
		})
		return
	}
	indexed, err := s.indexedProjectsAt(gstore, trunkTip)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	projects := index.AffectedProjectInfos(indexed)
	rootInvalidation := index.RootInvalidation(indexed)
	if s.Processor != nil {
		rootInvalidation = append(rootInvalidation, s.Processor.RootInvalidationPatterns...)
	}
	result := affected.Compute(projects, paths, affected.Options{
		RootInvalidationPatterns: rootInvalidation,
		ProsePatterns:            index.Prose(indexed),
	})
	writeJSON(w, http.StatusOK, result)
}

// checkRunReport mirrors cli/runko-ci's CheckRunReport exactly (the POST
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
		DetailsURL: report.DetailsURL,
	}
	if err := s.Store.UpsertCheckRun(r.Context(), key, change.HeadSHA, run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A reported result may have flipped mergeability - the when-ready
	// land evaluates now, not at the next sweep tick.
	s.KickAutomerge()
	w.WriteHeader(http.StatusCreated)
}

// deployImageReport mirrors cli/runko-ci's ImageReport (and
// docs/spec/webhooks/image-report.schema.json) - the POST
// /api/deploys/{sha}/images body report-image round-trips against.
type deployImageReport struct {
	Image    string `json:"image"`
	ImageRef string `json:"image_ref"`
	Digest   string `json:"digest"`
	RunURL   string `json:"run_url"`
	Reporter string `json:"reporter"`
}

// handlePostDeployImage records one built image's digest against a landed
// commit's deploy record (§14.10 inverted CD trigger). When the report
// completes the record's expected image set, it emits deploy.images_ready -
// the runko-deployer pins the digests into the GitOps repo and Argo CD rolls.
// A report for a sha with no open record is 404: the expected set is only
// known at land (an unaffected/docs-only land opens no record).
func (s *Server) handlePostDeployImage(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha")
	var report deployImageReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, fmt.Sprintf("decode image report: %v", err), http.StatusBadRequest)
		return
	}
	if report.Image == "" || report.Digest == "" {
		http.Error(w, "image and digest are required", http.StatusBadRequest)
		return
	}
	rec, ok, nowReady, err := s.Store.RecordDeployImage(r.Context(), sha, DeployImageRow{
		Image: report.Image, ImageRef: report.ImageRef, Digest: report.Digest, RunURL: report.RunURL,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no deploy record for this commit (it landed nothing deployable, or was not landed here)", http.StatusNotFound)
		return
	}
	if nowReady {
		s.enqueueDeployImagesReadyWebhook(r.Context(), rec)
	}
	w.WriteHeader(http.StatusCreated)
}

// enqueueDeployImagesReadyWebhook emits deploy.images_ready once a landed
// commit's every affected image has reported its digest (§14.10). Best-effort
// like the other webhook enqueues: the record is already durably ready, so a
// failed enqueue must not fail the report request (the outbox owns retries).
func (s *Server) enqueueDeployImagesReadyWebhook(ctx context.Context, rec DeployRecord) {
	images := make([]checks.DeployImage, 0, len(rec.Reported))
	for _, img := range rec.Reported {
		images = append(images, checks.DeployImage{Image: img.Image, ImageRef: img.ImageRef, Digest: img.Digest})
	}
	hook := checks.DeployImagesReadyWebhook{
		SpecVersion: "1",
		DeliveryID:  rec.TrunkSHA + "@images_ready",
		Type:        "deploy.images_ready",
		OccurredAt:  s.clock(),
		OrgID:       s.SettingsOrg,
		Deploy: checks.DeployImages{
			TrunkSHA:   rec.TrunkSHA,
			ChangeKey:  rec.ChangeKey,
			Images:     images,
			Provenance: rec.Provenance,
		},
	}
	payload, err := json.Marshal(hook)
	if err != nil {
		log.Printf("runkod: %s: marshal deploy.images_ready webhook: %v", rec.TrunkSHA, err)
		return
	}
	if _, err := s.Store.EnqueueWebhook(ctx, hook.Type, payload); err != nil {
		log.Printf("runkod: %s: enqueue deploy.images_ready webhook: %v", rec.TrunkSHA, err)
	}
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
	// Optional body: {"force": true} is the §13.5 admin override. The
	// historical body-less POST stays valid (force defaults to false).
	var body struct {
		Force bool `json:"force"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // empty/absent body is fine
	}
	decision, apiErr := s.landChangeCore(r.Context(), key, change, s.laneFor(r), s.principalFor(r), body.Force)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}

	switch {
	case decision.Landed:
		writeJSON(w, http.StatusOK, landResponse{Landed: true, LandedSHA: decision.LandedSHA, Forced: decision.Forced})
	case decision.RequiresRevalidation:
		writeJSON(w, http.StatusConflict, &clierr.Error{
			Code:       "requires_revalidation",
			Field:      "change",
			Message:    "trunk has moved in a way that intersects this change's affected set",
			Suggestion: "re-run required checks against current trunk, then retry land",
			DocURL:     "docs/design.md#135-optimistic-revalidation",
		})
	case len(decision.Conflicts) > 0:
		writeJSON(w, http.StatusConflict, &clierr.Error{
			Code:       "merge_conflict",
			Field:      "change",
			Message:    fmt.Sprintf("rebase produced conflicts in: %s", strings.Join(decision.Conflicts, ", ")),
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
	// Forced marks a land that bypassed the merge gates via the admin
	// override (§13.5) - also durable on the Change as landed_forced.
	Forced bool `json:",omitempty"`
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
		OrgID:       s.SettingsOrg,
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
	if trunkTip, rerr := gstore.ResolveRef("refs/heads/" + s.TrunkRef); rerr == nil {
		if indexed, ierr := s.indexedProjectsAt(gstore, trunkTip); ierr == nil {
			tagProjects(result, indexed)
		}
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
// cli/runko-ci shells out to, since this package has no dependency on that
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
