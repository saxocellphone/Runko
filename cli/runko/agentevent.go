// runko agent event / agent hooks - the §12.6.1 harness-reporting client.
// A coding harness's post-tool-use hook pipes its JSON here (--from-hook)
// or names the event explicitly (--kind/--detail); one invocation posts one
// event to POST /api/workspaces/{id}/activity (the endpoint takes a batch,
// so a future spooler is an API no-op). The workspace comes from the
// worktree's own runko.workspace config, the watch.go rule; credentials
// fall back to RUNKO_RUNKOD_URL/RUNKO_TOKEN env because hooks inherit an
// environment, not flags - the rule resolveCredentialEnv (auth.go) now
// applies to every control-plane verb.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/saxocellphone/runko/internal/clierr"
)

// agentEventTimeout bounds the report POST: telemetry must never hang the
// harness's tool loop on a slow daemon.
const agentEventTimeout = 3 * time.Second

// agentEventDetailMax pre-truncates detail client-side (the server cuts
// at 240 runes anyway; this keeps a pathological command line from
// shipping megabytes at the 1MB request cap).
const agentEventDetailMax = 500

// hookInput is the post-tool-use JSON a coding harness pipes to
// --from-hook (the Claude Code hooks shape; other harnesses fit by
// emitting the same three fields).
type hookInput struct {
	SessionID string `json:"session_id"`
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		FilePath     string `json:"file_path"`
		NotebookPath string `json:"notebook_path"`
		Command      string `json:"command"`
		Pattern      string `json:"pattern"`
	} `json:"tool_input"`
}

// eventFromHook maps a harness tool call onto the §12.6.1 soft kind
// vocabulary. Unknown tools become a note naming the tool - the server
// coerces unknown kinds the same way, but mapping here keeps the detail
// meaningful.
func eventFromHook(in hookInput) (kind, detail string) {
	switch in.ToolName {
	case "Read":
		return "read", in.ToolInput.FilePath
	case "Edit", "Write", "MultiEdit":
		return "edit", in.ToolInput.FilePath
	case "NotebookEdit":
		return "edit", in.ToolInput.NotebookPath
	case "Bash":
		return "command", in.ToolInput.Command
	case "Grep", "Glob":
		return "search", in.ToolInput.Pattern
	default:
		return "note", in.ToolName
	}
}

func newAgentEventCmd(a *app) *cobra.Command {
	var (
		kind, detail, session, dir string
		fromHook, jsonOut          bool
	)
	cmd := &cobra.Command{
		Use:   "event --kind <kind> --detail <text>",
		Short: "Report one activity event to the workspace feed",
		Long: `Reports what the agent is doing (kind read|edit|command|search|note +
detail) to the workspace's §12.6.1 live feed. --from-hook derives
kind/detail/session from a post-tool-use hook JSON on stdin - the form
` + "`runko agent hooks`" + ` wires. Observability only; it never gates.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromHook {
				var in hookInput
				if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in); err != nil {
					return &clierr.Error{
						Code: "invalid_hook_input", Field: "stdin",
						Message:    fmt.Sprintf("stdin is not a post-tool-use hook JSON: %v", err),
						Suggestion: "wire this via `runko agent hooks`, or pass --kind/--detail explicitly",
					}
				}
				hookKind, hookDetail := eventFromHook(in)
				if kind == "" {
					kind = hookKind
				}
				if detail == "" {
					detail = hookDetail
				}
				if session == "" {
					session = in.SessionID
				}
			}
			if kind == "" || detail == "" {
				return usageError("usage: runko agent event --kind <read|edit|command|search|note> --detail <text>  (or --from-hook with hook JSON on stdin)")
			}
			if r := []rune(detail); len(r) > agentEventDetailMax {
				detail = string(r[:agentEventDetailMax])
			}

			id, _ := runGit(dir, "config", "runko.workspace")
			if id == "" {
				return &clierr.Error{
					Code: "not_a_workspace", Field: "dir",
					Message:    fmt.Sprintf("%s is not bound to a runko workspace", dir),
					Suggestion: "run inside a `runko workspace create/attach` checkout (--jj for a jj colocated clone), or bind one with `git config runko.workspace <id>`",
				}
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(context.Background(), agentEventTimeout)
			defer cancel()
			var out struct {
				Recorded int `json:"recorded"`
			}
			if err := apiJSON(ctx, http.DefaultClient, http.MethodPost,
				strings.TrimSuffix(cred.URL, "/")+"/api/workspaces/"+id+"/activity", cred.AuthHeader(),
				map[string]any{"events": []map[string]string{{
					"kind": kind, "detail": detail, "session_id": session,
				}}}, &out); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(out)
			}
			fmt.Printf("reported %s: %s\n", kind, detail)
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&kind, "kind", "", "event kind: read|edit|command|search|note")
	fl.StringVar(&detail, "detail", "", "what happened: a path, a command line")
	fl.StringVar(&session, "session", "", "harness coding-session id (§7.2 audit link)")
	fl.BoolVar(&fromHook, "from-hook", false, "read a post-tool-use hook JSON from stdin and derive kind/detail/session from it")
	fl.StringVar(&dir, "dir", ".", "workspace worktree to report for")
	fl.BoolVar(&jsonOut, "json", false, "emit the result as JSON")
	return cmd
}

// agentHooksSnippet is the ready-to-paste harness hooks config `runko
// agent hooks` prints: one PostToolUse matcher, because --from-hook maps
// tools to kinds itself. `|| true` keeps a missing credential or closed
// workspace from ever disturbing the agent's tool loop - telemetry is
// best-effort by decision (§12.6.1).
const agentHooksSnippet = `{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Read|Edit|Write|MultiEdit|NotebookEdit|Bash|Grep|Glob",
        "hooks": [
          { "type": "command", "command": "runko agent event --from-hook || true" }
        ]
      }
    ]
  }
}`

func newAgentHooksCmd() *cobra.Command {
	var (
		install, jsonOut bool
		dir              string
	)
	cmd := &cobra.Command{
		Use:   "hooks [--install]",
		Short: "Print or install the harness hooks snippet",
		Long: `Prints the ready-to-paste harness hooks JSON (prerequisites on
stderr, snippet on stdout). --install merges it into the worktree's
Claude Code .claude/settings.local.json instead: foreign keys survive,
an already-wired file no-ops, and the file is excluded from snapshots.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !install {
				// Prerequisites go to stderr so `runko agent hooks > hooks.json`
				// captures pure JSON.
				fmt.Fprintln(os.Stderr, "merge this into your harness settings (Claude Code: `runko agent hooks --install`")
				fmt.Fprintln(os.Stderr, "does it for you, into .claude/settings.local.json).")
				fmt.Fprintln(os.Stderr, "prerequisites: runko on PATH; RUNKO_RUNKOD_URL + RUNKO_TOKEN exported in the")
				fmt.Fprintln(os.Stderr, "harness environment (or a stored `runko auth login`); name the workspace with")
				fmt.Fprintln(os.Stderr, "-w (or run inside its worktree). events feed the workspace page's live Agent")
				fmt.Fprintln(os.Stderr, "activity card (§12.6.1).")
				fmt.Println(agentHooksSnippet)
				return nil
			}

			// -w installs into the named workspace's materialization, so wiring a
			// worktree never costs a cd into it (§12.7).
			wd, err := resolveWorkspaceDir(mustWorkspaceFlag(cmd), dir)
			if err != nil {
				return err
			}
			path, installed, excludedVia, err := InstallAgentHooks(wd)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"path": path, "installed": installed})
			}
			if installed {
				fmt.Printf("installed PostToolUse hook -> %s   (merged; other settings untouched)\n", path)
			} else {
				fmt.Printf("hook already wired -> %s\n", path)
			}
			fmt.Printf("excluded from snapshots via %s\n", excludedVia)
			// The installer cannot fix the harness environment - hooks inherit
			// env, not flags (§12.6.1) - but it can say exactly what's missing.
			if _, err := resolveCredentialEnv("", ""); err != nil {
				fmt.Println("credentials: none resolve - export these in the harness environment:")
				fmt.Println("  export RUNKO_RUNKOD_URL=<your runkod url>")
				fmt.Println("  export RUNKO_TOKEN=<name>:<token>")
			} else {
				fmt.Println("credentials: ok (env or stored login) - hooks will authenticate")
			}
			return nil
		},
	}
	fl := cmd.Flags()
	fl.BoolVar(&install, "install", false, "merge the snippet into this worktree's .claude/settings.local.json (Claude Code; other harnesses paste the printed snippet)")
	fl.StringVar(&dir, "dir", ".", "workspace worktree to install into")
	addWorkspaceFlag(cmd)
	fl.BoolVar(&jsonOut, "json", false, "with --install: emit {path,installed} as JSON")
	return cmd
}
