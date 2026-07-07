// Command runkod is the write-path daemon (docs/design.md §28.3 DAG stage
// 10): smart-HTTP hosting of one bare monorepo, real pre-receive wiring to
// receive.Decide(), a gitleaks-backed SecretScanner, REST endpoints
// (changes/checks/affected/merge-requirements), and a webhook outbox
// delivery worker. See runkod/doc.go for this session's scope boundaries
// (one monorepo per daemon, deploy-token bearer auth, in-memory Store).
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

	store := runkod.NewMemStore()
	processor := &runkod.Processor{RepoDir: *repoDir, TrunkRef: *trunk, Scanner: scanner, Store: store}
	server := &runkod.Server{RepoDir: *repoDir, TrunkRef: *trunk, Store: store, Processor: processor, Token: *token}
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
