// runko agent event / agent hooks - the §12.6.1 harness-reporting client.
// A coding harness's post-tool-use hook pipes its JSON here (--from-hook)
// or names the event explicitly (--kind/--detail); one invocation posts one
// event to POST /api/workspaces/{id}/activity (the endpoint takes a batch,
// so a future spooler is an API no-op). The workspace comes from the
// worktree's own runko.workspace config, the watch.go rule; credentials
// fall back to RUNKO_RUNKOD_URL/RUNKO_TOKEN env because hooks inherit an
// environment, not flags.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

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

// resolveAgentEventCredential is resolveCredential plus a verb-local env
// fallback (flags > RUNKO_RUNKOD_URL/RUNKO_TOKEN > stored login): a hook
// runs in whatever environment the harness inherited, and exporting two
// variables there is the whole setup.
func resolveAgentEventCredential(urlFlag, tokenFlag string) (Credential, error) {
	if tokenFlag == "" {
		if envToken := os.Getenv("RUNKO_TOKEN"); envToken != "" {
			envURL := os.Getenv("RUNKO_RUNKOD_URL")
			if urlFlag != "" {
				envURL = urlFlag
			}
			if envURL == "" {
				return Credential{}, &clierr.Error{
					Code: "missing_url", Field: "runkod-url",
					Message:    "RUNKO_TOKEN is set without RUNKO_RUNKOD_URL",
					Suggestion: "export RUNKO_RUNKOD_URL=<url> alongside RUNKO_TOKEN",
				}
			}
			return Credential{URL: envURL, Secret: envToken}, nil
		}
	}
	return resolveCredential(urlFlag, tokenFlag)
}

func cmdAgentEvent(rest []string) error {
	fs := flag.NewFlagSet("agent event", flag.ExitOnError)
	kind := fs.String("kind", "", "event kind: read|edit|command|search|note")
	detail := fs.String("detail", "", "what happened: a path, a command line")
	session := fs.String("session", "", "harness coding-session id (§7.2 audit link)")
	fromHook := fs.Bool("from-hook", false, "read a post-tool-use hook JSON from stdin and derive kind/detail/session from it")
	dir := fs.String("dir", ".", "workspace worktree to report for")
	runkodURL := fs.String("runkod-url", "", "runkod base URL")
	token := fs.String("token", "", "deploy token")
	jsonOut := fs.Bool("json", false, "emit the result as JSON")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	if *fromHook {
		var in hookInput
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&in); err != nil {
			return &clierr.Error{
				Code: "invalid_hook_input", Field: "stdin",
				Message:    fmt.Sprintf("stdin is not a post-tool-use hook JSON: %v", err),
				Suggestion: "wire this via `runko agent hooks`, or pass --kind/--detail explicitly",
			}
		}
		hookKind, hookDetail := eventFromHook(in)
		if *kind == "" {
			*kind = hookKind
		}
		if *detail == "" {
			*detail = hookDetail
		}
		if *session == "" {
			*session = in.SessionID
		}
	}
	if *kind == "" || *detail == "" {
		return usageError("usage: runko agent event --kind <read|edit|command|search|note> --detail <text>  (or --from-hook with hook JSON on stdin)")
	}
	if r := []rune(*detail); len(r) > agentEventDetailMax {
		*detail = string(r[:agentEventDetailMax])
	}

	id, _ := runGit(*dir, "config", "runko.workspace")
	if id == "" {
		return &clierr.Error{
			Code: "not_a_workspace", Field: "dir",
			Message:    fmt.Sprintf("%s is not bound to a runko workspace", *dir),
			Suggestion: "run inside a `runko workspace create/attach` worktree, or bind a clone with `git config runko.workspace <id>`",
		}
	}
	cred, err := resolveAgentEventCredential(*runkodURL, *token)
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
			"kind": *kind, "detail": *detail, "session_id": *session,
		}}}, &out); err != nil {
		return err
	}
	if *jsonOut {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	fmt.Printf("reported %s: %s\n", *kind, *detail)
	return nil
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

func cmdAgentHooks(rest []string) error {
	fs := flag.NewFlagSet("agent hooks", flag.ExitOnError)
	if err := fs.Parse(rest); err != nil {
		return err
	}
	// Prerequisites go to stderr so `runko agent hooks > hooks.json`
	// captures pure JSON.
	fmt.Fprintln(os.Stderr, "merge this into your harness settings (Claude Code: .claude/settings.json).")
	fmt.Fprintln(os.Stderr, "prerequisites: runko on PATH; RUNKO_RUNKOD_URL + RUNKO_TOKEN exported in the")
	fmt.Fprintln(os.Stderr, "harness environment (or a stored `runko auth login`); run inside a workspace")
	fmt.Fprintln(os.Stderr, "worktree. events feed the workspace page's live Agent activity card (§12.6.1).")
	fmt.Println(agentHooksSnippet)
	return nil
}
