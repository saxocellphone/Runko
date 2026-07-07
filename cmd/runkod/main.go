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
	"strings"
	"time"

	"github.com/saxocellphone/runko/receive"
	"github.com/saxocellphone/runko/runkod"
	"github.com/saxocellphone/runko/search"
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
	repoDir := fs.String("repo-dir", "", "path to the bare monorepo (created if it doesn't exist)")
	addr := fs.String("addr", "127.0.0.1:8080", "listen address")
	trunk := fs.String("trunk", "main", "trunk ref name")
	token := fs.String("token", "", "deploy token: REST API bearer auth + pre-receive shared secret")
	webhookURL := fs.String("webhook-url", "", "webhook delivery target (optional - outbox worker is idle without one)")
	webhookSecret := fs.String("webhook-secret", "", "webhook HMAC secret")
	gitleaksBin := fs.String("gitleaks-bin", "gitleaks", "gitleaks binary (path or PATH-resolved name)")
	skipScan := fs.Bool("insecure-skip-secret-scan", false, "DEV/EVAL ONLY: disable secret scanning entirely (never use in production, docs/design.md §11.4)")
	databaseURL := fs.String("database-url", "", "Postgres DSN for durable storage (default: in-memory Store, the §9.3 Eval/dev profile - lost on restart)")
	rootInvalidation := fs.String("root-invalidation", "", "comma-separated root-invalidation glob patterns (org policy, §14.5.2)")
	globalChecks := fs.String("global-required-checks", "", "comma-separated org-level check names required on EVERY change (§14.9, e.g. secrets-scan)")
	allowUnpoliced := fs.Bool("insecure-allow-unpoliced-land", false, "DEV/EVAL ONLY: let changes that resolve NO merge policy (no required checks, no owners) land anyway - the in-memory eval profile implies this; a durable deployment should declare policy instead (§28.3 stage 11c)")
	var botLanes botLaneFlag
	fs.Var(&botLanes, "bot-lane", "path-scoped auto-land grant (§14.10.2), repeatable: 'name=<n>;token=<t>;paths=<glob,glob>;checks=<check,check>'")
	searchURL := fs.String("search-url", "", "zoekt-webserver base URL for search_code (§8.3); absent -> search_code returns a structured 'not configured' error, never a git-grep fallback (§8.2)")
	zoektIndexDir := fs.String("zoekt-index-dir", "", "directory zoekt-git-index writes shards into (required to enable indexing on trunk advance)")
	zoektIndexBin := fs.String("zoekt-index-bin", "zoekt-git-index", "zoekt-git-index binary (path or PATH-resolved name)")
	if err := fs.Parse(args); err != nil {
		return err
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

	var store runkod.Store
	if *databaseURL != "" {
		orgName := strings.TrimSuffix(runkod.RepoMountName(*repoDir), ".git")
		pg, err := runkod.BootstrapPostgresStore(context.Background(), *databaseURL, orgName, *trunk)
		if err != nil {
			return fmt.Errorf("serve: %w", err)
		}
		store = pg
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

	processor := &runkod.Processor{
		RepoDir: *repoDir, TrunkRef: *trunk, Scanner: scanner, Store: store,
		RootInvalidationPatterns: splitNonEmpty(*rootInvalidation),
		ZoektIndexWorker:         indexWorker,
	}
	server := &runkod.Server{
		RepoDir: *repoDir, TrunkRef: *trunk, Store: store, Processor: processor, Token: *token, Searcher: searcher,
		GlobalRequiredChecks: splitNonEmpty(*globalChecks),
		AllowUnpolicedLand:   *allowUnpoliced,
		BotLanes:             botLanes,
	}
	handler, err := server.Handler()
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if *webhookURL != "" {
		worker := &runkod.OutboxWorker{Store: store, URL: *webhookURL, Secret: []byte(*webhookSecret)}
		go worker.Run(ctx, 5*time.Second)
	}
	if indexWorker != nil {
		indexWorker.Trigger() // index whatever trunk already holds at startup, not just future advances
	}

	fmt.Printf("runkod: serving %s at %s (clone: %s/%s)\n", *repoDir, selfURL, selfURL, runkod.RepoMountName(*repoDir))
	return http.Serve(ln, handler)
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
		default:
			return lane, fmt.Errorf("bot-lane: unknown key %q (want name, token, paths, checks)", key)
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
