// Command runkod is the write-path daemon (docs/design.md §28.3 DAG stage
// 10): smart-HTTP hosting of one bare monorepo, real pre-receive wiring to
// receive.Decide(), a gitleaks-backed SecretScanner, REST endpoints
// (changes/checks/affected/merge-requirements/search), and a webhook outbox
// delivery worker. See runkod/doc.go for this session's scope boundaries
// (one monorepo per daemon, deploy-token bearer auth, in-memory Store).
//
// search_code (§8.3, §28.3 stage 11) is wired the same way: --search-url
// points at a real zoekt-webserver (search.ZoektSearcher, a stdlib HTTP
// client - see search/doc.go for why this is a process, not a Go library
// dependency); --zoekt-index-dir enables a debounced zoekt-git-index run on
// trunk advance (runkod.ZoektIndexWorker). Neither flag is required - absent
// --search-url, GET /api/search returns a structured "not configured"
// error, deliberately never a git-grep fallback (§8.2).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/saxocellphone/runko/platform/mirror"
	"github.com/saxocellphone/runko/platform/receive"
	"github.com/saxocellphone/runko/platform/search"
	"github.com/saxocellphone/runko/runkod"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	var err error
	switch os.Args[1] {
	case "serve":
		err = cmdServe(os.Args[2:])
	case "hook":
		cmdHook(os.Args[2:]) // handles its own exit code (git hook semantics), see below
		return
	case "-h", "--help", "help":
		printUsage()
		return
	default:
		fmt.Fprintf(os.Stderr, "runkod: unknown command %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "runkod: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `usage: runkod <command> [flags]

commands:
  serve   host one bare monorepo: smart-HTTP git, pre-receive wiring, REST API, webhook outbox
  hook    internal: invoked by the installed pre-receive hook, not for direct use`)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	// Every serve flag falls back to a RUNKO_<UPPER_SNAKE> env var (§9.4's
	// stage-14 convention): compose/k8s manifests configure the daemon
	// through environment, not argv templating. Precedence: flag > env >
	// default.
	repoDir := fs.String("repo-dir", envString("REPO_DIR", ""), "path to the bare monorepo (created if it doesn't exist) [RUNKO_REPO_DIR]")
	addr := fs.String("addr", envString("ADDR", "127.0.0.1:8080"), "listen address [RUNKO_ADDR]")
	trunk := fs.String("trunk", envString("TRUNK", "main"), "trunk ref name [RUNKO_TRUNK]")
	token := fs.String("token", envString("TOKEN", ""), "deploy token: REST API bearer auth + pre-receive shared secret [RUNKO_TOKEN]")
	webhookURL := fs.String("webhook-url", envString("WEBHOOK_URL", ""), "webhook delivery target (optional - outbox worker is idle without one) [RUNKO_WEBHOOK_URL]")
	webhookSecret := fs.String("webhook-secret", envString("WEBHOOK_SECRET", ""), "webhook HMAC secret [RUNKO_WEBHOOK_SECRET]")
	gitleaksBin := fs.String("gitleaks-bin", envString("GITLEAKS_BIN", "gitleaks"), "gitleaks binary (path or PATH-resolved name) [RUNKO_GITLEAKS_BIN]")
	skipScan := fs.Bool("insecure-skip-secret-scan", envBool("INSECURE_SKIP_SECRET_SCAN"), "DEV/EVAL ONLY: disable secret scanning entirely (never use in production, docs/design.md §11.4) [RUNKO_INSECURE_SKIP_SECRET_SCAN]")
	databaseURL := fs.String("database-url", envString("DATABASE_URL", ""), "Postgres DSN for durable storage (default: in-memory Store, the §9.3 Eval/dev profile - lost on restart) [RUNKO_DATABASE_URL]")
	rootInvalidation := fs.String("root-invalidation", envString("ROOT_INVALIDATION", ""), "comma-separated root-invalidation glob patterns (org policy, §14.5.2) [RUNKO_ROOT_INVALIDATION]")
	globalChecks := fs.String("global-required-checks", envString("GLOBAL_REQUIRED_CHECKS", ""), "comma-separated org-level check names required on EVERY change (§14.9, e.g. secrets-scan) [RUNKO_GLOBAL_REQUIRED_CHECKS]")
	allowSignup := fs.Bool("allow-signup", envBool("ALLOW_SIGNUP"), "enable self-service sign-up (POST /api/signup, §15.1) - default off [RUNKO_ALLOW_SIGNUP]")
	signupCode := fs.String("signup-code", envString("SIGNUP_CODE", ""), "invite code sign-ups must present (only meaningful with --allow-signup) [RUNKO_SIGNUP_CODE]")
	singleUseAgentWS := fs.Bool("single-use-agent-workspaces", envBoolDefault("SINGLE_USE_AGENT_WORKSPACES", true), "close an agent-owned workspace when its last open change lands or is abandoned - one workspace per task; the funnel then refuses pushes into it with a create-a-fresh-one suggestion (§8.7/§12.2). ON by default; humans are never affected [RUNKO_SINGLE_USE_AGENT_WORKSPACES]")
	allowUnpoliced := fs.Bool("insecure-allow-unpoliced-land", envBool("INSECURE_ALLOW_UNPOLICED_LAND"), "DEV/EVAL ONLY: let changes that resolve NO merge policy (no required checks, no owners) land anyway - the in-memory eval profile implies this; a durable deployment should declare policy instead (§28.3 stage 11c) [RUNKO_INSECURE_ALLOW_UNPOLICED_LAND]")
	allowWorkspaceless := fs.Bool("allow-workspaceless-changes", envBool("ALLOW_WORKSPACELESS_CHANGES"), "DEV/EVAL ONLY: accept refs/for pushes with no workspace origin - by default changes are born in workspaces (§12.2, 2026-07-09); a brand-new monorepo's bootstrap push is always exempt [RUNKO_ALLOW_WORKSPACELESS_CHANGES]")
	var botLanes botLaneFlag
	fs.Var(&botLanes, "bot-lane", "path-scoped auto-land grant (§14.10.2), repeatable: 'name=<n>;token=<t>;paths=<glob,glob>;checks=<check,check>' [RUNKO_BOT_LANES, '|'-separated]")
	var principals principalFlag
	fs.Var(&principals, "principal", "named-token identity (§15.1 interim), repeatable: 'name=<n>;token=<t>[;agent][;admin]' - agent principals get the default §8.7 agent policy enforced at receive; admin principals may force-land (§13.5) [RUNKO_PRINCIPALS, '|'-separated]")
	searchURL := fs.String("search-url", envString("SEARCH_URL", ""), "zoekt-webserver base URL for search_code (§8.3); absent -> search_code returns a structured 'not configured' error, never a git-grep fallback (§8.2) [RUNKO_SEARCH_URL]")
	zoektIndexDir := fs.String("zoekt-index-dir", envString("ZOEKT_INDEX_DIR", ""), "directory zoekt-git-index writes shards into (required to enable indexing on trunk advance) [RUNKO_ZOEKT_INDEX_DIR]")
	zoektIndexBin := fs.String("zoekt-index-bin", envString("ZOEKT_INDEX_BIN", "zoekt-git-index"), "zoekt-git-index binary (path or PATH-resolved name) [RUNKO_ZOEKT_INDEX_BIN]")
	mirrorRemote := fs.String("mirror-remote", envString("MIRROR_REMOTE", ""), "outbound mirror git URL (§18.6 M1) - ANY git host: https:// gets token auth, everything else used as-is [RUNKO_MIRROR_REMOTE]")
	mirrorUsername := fs.String("mirror-username", envString("MIRROR_USERNAME", ""), "basic-auth username for the mirror token: GitHub 'x-access-token' (default), GitLab 'oauth2', Gitea anything [RUNKO_MIRROR_USERNAME]")
	mirrorToken := fs.String("mirror-token", envString("MIRROR_TOKEN", ""), "mirror access token (prefer the env var - never lands in shell history) [RUNKO_MIRROR_TOKEN]")
	allowOrgCreate := fs.Bool("allow-org-create", envBool("ALLOW_ORG_CREATE"), "enable self-service org creation (POST /api/orgs, §7.1) - each org owns its own repo under --orgs-dir; default off [RUNKO_ALLOW_ORG_CREATE]")
	orgsDir := fs.String("orgs-dir", envString("ORGS_DIR", ""), "directory holding per-org repos at <orgs-dir>/<org>/repo.git (default: 'orgs' beside --repo-dir) [RUNKO_ORGS_DIR]")
	var orgMirrors orgMirrorFlag
	fs.Var(&orgMirrors, "org-mirror", "outbound mirror for a hub org's repo (§18.6 M1), repeatable: 'org=<name>;remote=<url>[;username=<u>][;token=<t>]' - the default org keeps --mirror-remote [RUNKO_ORG_MIRRORS, '|'-separated]")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Repeatable flags can't carry a value-bearing default, so their env
	// fallback applies only when the flag was never passed. '|' separates
	// entries because ';' and ',' are already meaningful inside one.
	if len(principals) == 0 {
		for _, v := range splitPipe(os.Getenv("RUNKO_PRINCIPALS")) {
			if err := principals.Set(v); err != nil {
				return fmt.Errorf("serve: RUNKO_PRINCIPALS: %w", err)
			}
		}
	}
	if len(botLanes) == 0 {
		for _, v := range splitPipe(os.Getenv("RUNKO_BOT_LANES")) {
			if err := botLanes.Set(v); err != nil {
				return fmt.Errorf("serve: RUNKO_BOT_LANES: %w", err)
			}
		}
	}
	if len(orgMirrors) == 0 {
		for _, v := range splitPipe(os.Getenv("RUNKO_ORG_MIRRORS")) {
			if err := orgMirrors.Set(v); err != nil {
				return fmt.Errorf("serve: RUNKO_ORG_MIRRORS: %w", err)
			}
		}
	}
	if *repoDir == "" {
		return fmt.Errorf("serve: --repo-dir is required")
	}
	if *token == "" {
		return fmt.Errorf("serve: --token is required")
	}

	var scanner receive.SecretScanner
	if *skipScan {
		fmt.Fprintln(os.Stderr, "runkod: WARNING --insecure-skip-secret-scan is set - no secret scanning will occur. Never use this in production (docs/design.md §11.4).")
		scanner = receive.NoOpScanner{}
	} else {
		if _, err := exec.LookPath(*gitleaksBin); err != nil {
			return fmt.Errorf("serve: gitleaks binary %q not found: install gitleaks, pass --gitleaks-bin, or (dev/eval only) pass --insecure-skip-secret-scan", *gitleaksBin)
		}
		scanner = runkod.GitleaksScanner{Bin: *gitleaksBin}
	}

	if err := runkod.EnsureBareRepo(*repoDir, *trunk); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("serve: listen on %s: %w", *addr, err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return fmt.Errorf("serve: determine listen port: %w", err)
	}
	selfURL := fmt.Sprintf("http://127.0.0.1:%s", port)

	if err := runkod.InstallPreReceiveHook(*repoDir, selfURL, *token); err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	defaultOrgName := strings.TrimSuffix(runkod.RepoMountName(*repoDir), ".git")
	var store runkod.Store
	var pgDefault *runkod.PostgresStore
	if *databaseURL != "" {
		pg, err := runkod.BootstrapPostgresStore(context.Background(), *databaseURL, defaultOrgName, *trunk)
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		store = pg
		pgDefault = pg
		fmt.Println("runkod: using Postgres-backed storage (durable across restarts)")
		if *allowUnpoliced {
			fmt.Fprintln(os.Stderr, "runkod: WARNING --insecure-allow-unpoliced-land is set - changes resolving NO merge policy (no required checks, no owners) will land ungated. Declare owners/ci.checks or --global-required-checks instead in production (§28.3 stage 11c).")
		}
	} else {
		fmt.Fprintln(os.Stderr, "runkod: WARNING no --database-url given - using in-memory storage (§9.3 Eval/dev profile): every Change, check run, and queued webhook is LOST on restart, and unpoliced changes (no required checks, no owners) may land ungated.")
		store = runkod.NewMemStore()
		// The eval profile is for kicking the tires (§16.4's compose loop
		// must work before any policy exists) - default-deny there would
		// make the first ever land impossible to demo.
		*allowUnpoliced = true
	}

	var indexWorker *runkod.ZoektIndexWorker
	if *zoektIndexDir != "" {
		if _, err := exec.LookPath(*zoektIndexBin); err != nil {
			return fmt.Errorf("serve: zoekt-git-index binary %q not found: install Zoekt or pass --zoekt-index-bin", *zoektIndexBin)
		}
		indexWorker = &runkod.ZoektIndexWorker{
			Indexer:  search.ZoektIndexer{Bin: *zoektIndexBin, IndexDir: *zoektIndexDir},
			RepoDir:  *repoDir,
			Debounce: 5 * time.Second,
		}
	}

	var searcher search.CodeSearcher
	if *searchURL != "" {
		searcher = search.ZoektSearcher{BaseURL: *searchURL}
	} else {
		fmt.Fprintln(os.Stderr, "runkod: no --search-url given - search_code (GET /api/search) will return a structured 'not configured' error (§8.2: no git-grep fallback)")
		searcher = search.NotConfiguredSearcher{}
	}

	// Per-org mirror workers, built in NewOrgServer (which knows the org's
	// repo dir + store) and started in StartOrgWorkers (which owns worker
	// lifecycle) - a sync.Map is the join between the two hub callbacks.
	var orgMirrorWorkers sync.Map
	for _, cfg := range orgMirrors {
		if cfg.Org == defaultOrgName {
			return fmt.Errorf("serve: --org-mirror names the default org %q - use --mirror-remote for it", defaultOrgName)
		}
	}

	var mirrorWorker *runkod.MirrorWorker
	if *mirrorRemote != "" {
		mirrorWorker = &runkod.MirrorWorker{
			Remote:   &mirror.Remote{RepoDir: *repoDir, URL: *mirrorRemote, Username: *mirrorUsername, Token: *mirrorToken},
			Store:    store,
			TrunkRef: *trunk,
			Debounce: 3 * time.Second,
			Interval: time.Minute,
		}
	}

	processor := &runkod.Processor{
		RepoDir: *repoDir, TrunkRef: *trunk, Scanner: scanner, Store: store,
		RootInvalidationPatterns: splitNonEmpty(*rootInvalidation),
		ZoektIndexWorker:         indexWorker,
		Mirror:                   mirrorWorker,
		Principals:               principals,
		BotLanes:                 botLanes,
		OrgName:                  defaultOrgName,
		RequireChangeWorkspace:   !*allowWorkspaceless,
	}
	server := &runkod.Server{
		RepoDir: *repoDir, TrunkRef: *trunk, Store: store, Processor: processor, Token: *token, Searcher: searcher,
		GlobalRequiredChecks:     splitNonEmpty(*globalChecks),
		SingleUseAgentWorkspaces: *singleUseAgentWS,
		AllowUnpolicedLand:       *allowUnpoliced,
		AllowSignup:              *allowSignup,
		SignupCode:               *signupCode,
		BotLanes:                 botLanes,
		Principals:               principals,
		Mirror:                   mirrorWorker,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Multi-org (§7.1, runkod/orghub.go): the root-mounted repo above is
	// the default org; hub-created orgs each own a repo under --orgs-dir,
	// mounted at /o/<name>/ with the identical Server surface.
	if *orgsDir == "" {
		*orgsDir = filepath.Join(filepath.Dir(strings.TrimRight(*repoDir, "/")), "orgs")
	}
	directory, ok := store.(runkod.Directory)
	if !ok {
		return fmt.Errorf("serve: store %T does not implement the account directory", store)
	}
	// The default org is an org like any other (org-scoped sessions,
	// 2026-07-09): store-backed accounts need a membership row to reach
	// it - the historical everyone-with-a-credential behavior is gone.
	// Operator principals and the deploy token stay server-wide, so CI,
	// hooks, and the eval loop are unaffected; signups join it explicitly
	// (org_mode=join) like any org.
	server.Directory = directory
	server.SettingsOrg = defaultOrgName
	server.OrgName = defaultOrgName
	hub := &runkod.OrgHub{
		Default:        server,
		DefaultOrgName: defaultOrgName,
		DataDir:        *orgsDir,
		SelfURL:        selfURL,
		AllowOrgCreate: *allowOrgCreate,
		Directory:      directory,
		Ctx:            ctx,
		NewOrgStore: func(ctx context.Context, orgName string) (runkod.Store, error) {
			if pgDefault != nil {
				return runkod.NewOrgPostgresStore(ctx, pgDefault.Pool, orgName, *trunk)
			}
			return runkod.NewMemStore(), nil
		},
		NewOrgServer: func(orgName, orgRepoDir string, orgStore runkod.Store) (*runkod.Server, error) {
			proc := &runkod.Processor{
				RepoDir: orgRepoDir, TrunkRef: *trunk, Scanner: scanner, Store: orgStore,
				RootInvalidationPatterns: splitNonEmpty(*rootInvalidation),
				Principals:               principals,
				BotLanes:                 botLanes,
				Directory:                directory,
				OrgName:                  orgName,
				RequireChangeWorkspace:   !*allowWorkspaceless,
			}
			// Per-org mirror (§18.6 M1, was default-org-only in v1 -
			// docs/migration-findings.md #12): an --org-mirror entry naming
			// this org gets its own MirrorWorker over the org's repo and
			// org-scoped Store (cursors live per org). Setting it on the
			// Server lights up /o/<org>/api/mirror/status|unfreeze; the
			// worker starts in StartOrgWorkers. Per-org zoekt stays
			// default-org-only; search answers structured not-configured.
			var orgMirror *runkod.MirrorWorker
			for _, cfg := range orgMirrors {
				if cfg.Org != orgName {
					continue
				}
				orgMirror = &runkod.MirrorWorker{
					Remote:   &mirror.Remote{RepoDir: orgRepoDir, URL: cfg.Remote, Username: cfg.Username, Token: cfg.Token},
					Store:    orgStore,
					TrunkRef: *trunk,
					Debounce: 3 * time.Second,
					Interval: time.Minute,
				}
				proc.Mirror = orgMirror
				orgMirrorWorkers.Store(orgName, orgMirror)
				break
			}
			return &runkod.Server{
				RepoDir: orgRepoDir, TrunkRef: *trunk, Store: orgStore, Processor: proc, Token: *token,
				Searcher:                 search.NotConfiguredSearcher{},
				GlobalRequiredChecks:     splitNonEmpty(*globalChecks),
				SingleUseAgentWorkspaces: *singleUseAgentWS,
				AllowUnpolicedLand:       *allowUnpoliced,
				BotLanes:                 botLanes,
				Principals:               principals,
				Mirror:                   orgMirror,
			}, nil
		},
	}
	if *webhookURL != "" || len(orgMirrors) > 0 {
		hub.StartOrgWorkers = func(ctx context.Context, orgName string, orgStore runkod.Store) {
			if *webhookURL != "" {
				worker := &runkod.OutboxWorker{Store: orgStore, URL: *webhookURL, Secret: []byte(*webhookSecret)}
				go worker.Run(ctx, 5*time.Second)
			}
			if w, ok := orgMirrorWorkers.Load(orgName); ok {
				worker := w.(*runkod.MirrorWorker)
				go worker.Run(ctx)
				worker.Trigger() // sync whatever the org repo already holds
				fmt.Printf("runkod: org %s mirroring (trunk leased, tags, change refs; workspace snapshots never)\n", orgName)
			}
		}
	}
	if loaded, err := hub.LoadExisting(ctx); err != nil {
		return fmt.Errorf("serve: %w", err)
	} else if len(loaded) > 0 {
		fmt.Printf("runkod: reattached %d org(s): %s\n", len(loaded), strings.Join(loaded, ", "))
	}
	if *allowOrgCreate {
		fmt.Println("runkod: self-service org creation enabled (POST /api/orgs); per-org repos under", *orgsDir)
	}

	handler, err := hub.Handler()
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	if *webhookURL != "" {
		worker := &runkod.OutboxWorker{Store: store, URL: *webhookURL, Secret: []byte(*webhookSecret)}
		go worker.Run(ctx, 5*time.Second)
	}
	if indexWorker != nil {
		indexWorker.Trigger() // index whatever trunk already holds at startup, not just future advances
	}
	if mirrorWorker != nil {
		go mirrorWorker.Run(ctx) // reconcile loop; restarts and missed triggers self-heal
		mirrorWorker.Trigger()   // sync whatever exists at startup
		fmt.Printf("runkod: mirroring to %s (trunk leased, tags, change refs; workspace snapshots never)\n", *mirrorRemote)
	}

	fmt.Printf("runkod: serving %s at %s (clone: %s/%s)\n", *repoDir, selfURL, selfURL, runkod.RepoMountName(*repoDir))

	// Ops floor (§28.3 stage 12c-④): SIGINT/SIGTERM drain in-flight
	// requests (bounded - a hung push must not block shutdown forever)
	// instead of dropping them mid-land, and slow-loris connections can't
	// hold header slots open indefinitely. No global Read/WriteTimeout:
	// git smart-HTTP transfers are legitimately long-running.
	httpServer := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	sigCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	errCh := make(chan error, 1)
	go func() { errCh <- httpServer.Serve(ln) }()

	select {
	case err := <-errCh:
		return err
	case <-sigCtx.Done():
		fmt.Println("runkod: shutting down (draining in-flight requests)")
		cancel() // stop the outbox worker's polling
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("serve: shutdown: %w", err)
		}
		return nil
	}
}

// cmdHook implements the hidden `runkod hook pre-receive` subcommand the
// installed pre-receive hook script (hook.go) invokes: forward stdin (the
// ref-update lines real git feeds every pre-receive hook) to the daemon's
// /internal/pre-receive endpoint, print each ref's message (git relays hook
// stdout/stderr to the pushing client, prefixing each line "remote: " -
// note this means messages that already bake in their own "remote: " text,
// like receive.RejectDirectPush's, appear doubled to the client; a cosmetic
// wrinkle left as-is rather than second-guessing that established string
// format from a prior session), and exit non-zero if any ref was rejected -
// git aborts the WHOLE push atomically on a non-zero pre-receive exit.
func cmdHook(args []string) {
	if len(args) < 1 || args[0] != "pre-receive" {
		fmt.Fprintln(os.Stderr, "usage: runkod hook pre-receive --addr <url> --token <token>")
		os.Exit(2)
	}
	fs := flag.NewFlagSet("hook pre-receive", flag.ExitOnError)
	addr := fs.String("addr", "", "daemon base URL")
	token := fs.String("token", "", "shared secret")
	if err := fs.Parse(args[1:]); err != nil {
		os.Exit(2)
	}

	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runkod hook: read stdin: %v\n", err)
		os.Exit(1)
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(*addr, "/")+"/internal/pre-receive", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "runkod hook: build request: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Authorization", "Bearer "+*token)
	// Forward git's object-quarantine env (see runkod/api.go's header
	// constants, runkod/prereceive.go's ProcessBatch doc comment): these
	// are set by git receive-pack on THIS hook process only, and the
	// daemon (a separate process) cannot see a push's just-received,
	// not-yet-committed objects without them.
	if v := os.Getenv("GIT_OBJECT_DIRECTORY"); v != "" {
		req.Header.Set("X-Git-Object-Directory", v)
	}
	if v := os.Getenv("GIT_ALTERNATE_OBJECT_DIRECTORIES"); v != "" {
		req.Header.Set("X-Git-Alternate-Object-Directories", v)
	}
	// Forward the authenticated pusher's identity the same way (§15.1
	// interim principals, stage 12c): requireGitAuth injected REMOTE_USER
	// into the CGI env, http-backend and receive-pack passed it down to
	// this hook as ordinary process environment.
	if v := os.Getenv("REMOTE_USER"); v != "" {
		req.Header.Set("X-Runko-Remote-User", v)
	}
	// Bot-lane identity travels beside (never as) REMOTE_USER - lanes are
	// not principals, and overloading REMOTE_USER would silently subject
	// them to workspace owner checks and change attribution built for
	// humans (§14.10.3, stage 17: the tags gate is the consumer).
	if v := os.Getenv("REMOTE_LANE"); v != "" {
		req.Header.Set("X-Runko-Remote-Lane", v)
	}
	// Forward `git push -o` options the same way (§12.2 provenance:
	// runko change push stamps workspace=<id>/workspace-branch=<name>):
	// receive-pack exposes them to this hook process only, as
	// GIT_PUSH_OPTION_COUNT + GIT_PUSH_OPTION_<n>.
	if count, err := strconv.Atoi(os.Getenv("GIT_PUSH_OPTION_COUNT")); err == nil {
		for i := 0; i < count; i++ {
			req.Header.Add("X-Runko-Push-Option", os.Getenv(fmt.Sprintf("GIT_PUSH_OPTION_%d", i)))
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runkod hook: contact daemon at %s: %v\n", *addr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "runkod hook: daemon returned %d\n", resp.StatusCode)
		os.Exit(1)
	}

	var results []runkod.RefResult
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		fmt.Fprintf(os.Stderr, "runkod hook: decode response: %v\n", err)
		os.Exit(1)
	}

	allAccepted := true
	for _, r := range results {
		if r.Message != "" {
			fmt.Print(r.Message)
		}
		if !r.Accepted {
			allAccepted = false
		}
	}
	if !allAccepted {
		os.Exit(1)
	}
}

// envString / envBool implement the RUNKO_<NAME> env fallback for serve
// flags (§9.4). envBool treats "1"/"true" (any case) as true.
func envString(name, def string) string {
	if v := os.Getenv("RUNKO_" + name); v != "" {
		return v
	}
	return def
}

func envBool(name string) bool {
	v := strings.ToLower(os.Getenv("RUNKO_" + name))
	return v == "1" || v == "true"
}

// envBoolDefault is envBool for flags whose DEFAULT is true: an unset env
// var keeps the default; "0"/"false" opt out.
func envBoolDefault(name string, def bool) bool {
	v := strings.ToLower(os.Getenv("RUNKO_" + name))
	if v == "" {
		return def
	}
	return v == "1" || v == "true"
}

// splitPipe splits a '|'-separated env value, dropping empty entries.
func splitPipe(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(v, "|") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// orgMirrorConfig is one --org-mirror value: an outbound mirror (§18.6 M1)
// for a hub org's repo. The default org's mirror stays --mirror-remote;
// this closes the "per-org mirror is default-org-only" v1 limitation
// (docs/migration-findings.md #12) for hub orgs.
type orgMirrorConfig struct {
	Org      string
	Remote   string
	Username string
	Token    string
}

// orgMirrorFlag parses repeatable --org-mirror flags.
type orgMirrorFlag []orgMirrorConfig

func (m *orgMirrorFlag) String() string { return fmt.Sprintf("%d org mirror(s)", len(*m)) }

func (m *orgMirrorFlag) Set(v string) error {
	cfg, err := parseOrgMirror(v)
	if err != nil {
		return err
	}
	*m = append(*m, cfg)
	return nil
}

// parseOrgMirror parses one --org-mirror value, e.g.
// "org=runko;remote=https://github.com/acme/runko.git;username=x-access-token;token=ghp_x".
// Same key=value;... shape as --principal; username/token optional (ssh and
// path remotes need neither).
func parseOrgMirror(v string) (orgMirrorConfig, error) {
	var cfg orgMirrorConfig
	for _, kv := range strings.Split(v, ";") {
		key, val, hasVal := strings.Cut(kv, "=")
		if !hasVal {
			return cfg, fmt.Errorf("org-mirror: unknown key %q (want org=, remote=, username=, token=)", kv)
		}
		switch key {
		case "org":
			cfg.Org = val
		case "remote":
			cfg.Remote = val
		case "username":
			cfg.Username = val
		case "token":
			cfg.Token = val
		default:
			return cfg, fmt.Errorf("org-mirror: unknown key %q (want org=, remote=, username=, token=)", kv)
		}
	}
	if cfg.Org == "" || cfg.Remote == "" {
		return cfg, fmt.Errorf("org-mirror: org= and remote= are both required")
	}
	return cfg, nil
}

// principalFlag parses repeatable --principal flags into runkod.Principal
// values (§15.1's interim named-token registry, stage 12c).
type principalFlag []runkod.Principal

func (p *principalFlag) String() string { return fmt.Sprintf("%d principal(s)", len(*p)) }

func (p *principalFlag) Set(v string) error {
	principal, err := parsePrincipal(v)
	if err != nil {
		return err
	}
	*p = append(*p, principal)
	return nil
}

// parsePrincipal parses one --principal value, e.g. "name=alice;token=t1"
// or "name=bumpbot;token=t2;agent". Agent principals get the default §8.7
// policy - per-principal policy overrides are a tree concern (§9.4's "the
// tree owns policy"), not more daemon flags.
func parsePrincipal(v string) (runkod.Principal, error) {
	var principal runkod.Principal
	for _, kv := range strings.Split(v, ";") {
		key, val, hasVal := strings.Cut(kv, "=")
		switch {
		case key == "name" && hasVal:
			principal.Name = val
		case key == "token" && hasVal:
			principal.Token = val
		case key == "agent" && (!hasVal || val == "true"):
			principal.IsAgent = true
			principal.Policy = receive.DefaultAgentPolicy()
		case key == "admin" && (!hasVal || val == "true"):
			principal.Admin = true
		default:
			return principal, fmt.Errorf("principal: unknown key %q (want name=, token=, agent, admin)", kv)
		}
	}
	if principal.Name == "" || principal.Token == "" {
		return principal, fmt.Errorf("principal: name= and token= are both required")
	}
	return principal, nil
}

// botLaneFlag parses repeatable --bot-lane flags into runkod.BotLane values.
type botLaneFlag []runkod.BotLane

func (b *botLaneFlag) String() string { return fmt.Sprintf("%d bot lane(s)", len(*b)) }

func (b *botLaneFlag) Set(v string) error {
	lane, err := parseBotLane(v)
	if err != nil {
		return err
	}
	*b = append(*b, lane)
	return nil
}

// parseBotLane parses one --bot-lane value, e.g.
// "name=image-bumper;token=<t>;paths=deploy/**,charts/**;checks=manifest-lint".
// All four keys are mandatory: §14.10.2 defines a lane as constrained to a
// path allowlist AND a required-check set - a lane without its own checks
// would be an unchecked auto-land grant, which the design deliberately does
// not model.
func parseBotLane(v string) (runkod.BotLane, error) {
	var lane runkod.BotLane
	for _, kv := range strings.Split(v, ";") {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			return lane, fmt.Errorf("bot-lane: %q is not key=value", kv)
		}
		switch key {
		case "name":
			lane.Name = val
		case "token":
			lane.Token = val
		case "paths":
			lane.PathAllowlist = splitNonEmpty(val)
		case "checks":
			lane.RequiredChecks = splitNonEmpty(val)
		case "tags":
			// Optional (§14.10.3, stage 17): tag-namespace globs the lane
			// may write under enforce_tag_policy. Unlike the land grant's
			// four keys, absence just means "no tag grant".
			lane.TagAllowlist = splitNonEmpty(val)
		default:
			return lane, fmt.Errorf("bot-lane: unknown key %q (want name, token, paths, checks, tags)", key)
		}
	}
	if lane.Name == "" || lane.Token == "" || len(lane.PathAllowlist) == 0 || len(lane.RequiredChecks) == 0 {
		return lane, fmt.Errorf("bot-lane: name=, token=, paths=, and checks= are all required - a lane is constrained to a path allowlist AND a required-check set (docs/design.md §14.10.2)")
	}
	return lane, nil
}

func splitNonEmpty(csv string) []string {
	if csv == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(csv, ",") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}
