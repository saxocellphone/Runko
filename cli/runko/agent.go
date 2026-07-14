// runko agent - ephemeral task identities (runkod/agentprincipal.go).
// Agents come and go, often many at once: the harness mints one identity
// per task (`agent create --task fix-rail-alignment`), hands the agent the
// returned token, and the credential dies by TTL. The name embeds the
// task, so authored_by / workspace owner / the review UI's agent badge all
// answer "what was this agent doing". An agent credential can NEVER mint
// (the server refuses agents_cannot_mint - no self-replication).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"
)

// AgentIdentity mirrors runkod's agentPrincipalResponse.
type AgentIdentity struct {
	Name      string    `json:"name"`
	Task      string    `json:"task"`
	Token     string    `json:"token,omitempty"`
	CreatedBy string    `json:"created_by"`
	ExpiresAt time.Time `json:"expires_at"`
	Live      bool      `json:"live"`
	Revoked   bool      `json:"revoked"`
}

func cmdAgent(args []string) error {
	if len(args) < 1 {
		return usageError("usage: runko agent create|list|revoke|event|hooks ...")
	}
	sub, rest := args[0], args[1:]
	ctx := context.Background()
	switch sub {
	case "event":
		return cmdAgentEvent(rest)
	case "hooks":
		return cmdAgentHooks(rest)
	case "create":
		fs := flag.NewFlagSet("agent create", flag.ExitOnError)
		task := fs.String("task", "", "task slug the identity is minted for (lowercase, digits, dashes)")
		ttl := fs.Duration("ttl", 0, "credential lifetime (default 8h, capped at 168h)")
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		jsonOut := fs.Bool("json", false, "emit the identity as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *task == "" {
			return usageError("usage: runko agent create --task <slug> [--ttl 8h]")
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		var out AgentIdentity
		err = apiJSON(ctx, http.DefaultClient, http.MethodPost,
			strings.TrimSuffix(cred.URL, "/")+"/api/agents", cred.AuthHeader(),
			map[string]any{"task": *task, "ttl_seconds": int(ttl.Seconds())}, &out)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(out)
		}
		fmt.Printf("minted %s (task %s, expires %s)\n", out.Name, out.Task, out.ExpiresAt.Format(time.RFC3339))
		fmt.Printf("token (shown ONCE): %s\n", out.Token)
		fmt.Printf("use it:\n")
		fmt.Printf("  API/CLI:  --token %s:%s   (Basic name:token)\n", out.Name, out.Token)
		fmt.Printf("  git:      https://%s:%s@<host>/o/<org>/repo.git\n", out.Name, out.Token)
		// The §12.6 golden-path teach (decided 2026-07-14): the streaming
		// commands, with the exact exports hooks need - hooks inherit an
		// environment, not flags (§12.6.1).
		fmt.Printf("stream the work (once inside the workspace worktree):\n")
		fmt.Printf("  export RUNKO_RUNKOD_URL=%s RUNKO_TOKEN=%s:%s\n", cred.URL, out.Name, out.Token)
		fmt.Printf("  runko workspace watch &          # live WIP on the workspace page (§12.6)\n")
		fmt.Printf("  runko agent hooks --install      # live activity feed (§12.6.1)\n")
		return nil

	case "list":
		fs := flag.NewFlagSet("agent list", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		jsonOut := fs.Bool("json", false, "emit the list as JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		var list []AgentIdentity
		if err := apiJSON(ctx, http.DefaultClient, http.MethodGet,
			strings.TrimSuffix(cred.URL, "/")+"/api/agents", cred.AuthHeader(), nil, &list); err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(list)
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		for _, a := range list {
			state := "live"
			if a.Revoked {
				state = "revoked"
			} else if !a.Live {
				state = "expired"
			}
			fmt.Fprintf(tw, "%s\t%s\ttask: %s\tby %s\texpires %s\n", a.Name, state, a.Task, a.CreatedBy, a.ExpiresAt.Format(time.RFC3339))
		}
		return tw.Flush()

	case "revoke":
		fs := flag.NewFlagSet("agent revoke", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL")
		token := fs.String("token", "", "deploy token")
		jsonOut := fs.Bool("json", false, "emit the result as JSON")
		var name string
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			name, rest = rest[0], rest[1:]
		}
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if name == "" {
			return usageError("usage: runko agent revoke <name>")
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		if err := apiJSON(ctx, http.DefaultClient, http.MethodPost,
			strings.TrimSuffix(cred.URL, "/")+"/api/agents/"+name+"/revoke", cred.AuthHeader(), map[string]string{}, nil); err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(map[string]string{"revoked": name})
		}
		fmt.Printf("revoked %s\n", name)
		return nil

	default:
		return usageError("usage: runko agent create|list|revoke|event|hooks ...")
	}
}
