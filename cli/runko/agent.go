// runko agent - ephemeral task identities (runkod/agentprincipal.go).
// Agents come and go, often many at once: the harness mints one identity
// per task (`agent create --task fix-rail-alignment`), hands the agent the
// returned token, and the credential dies by TTL. The name embeds the
// task, so authored_by / workspace owner / the review UI's agent badge all
// answer "what was this agent doing". An agent credential can NEVER mint
// (the server refuses agents_cannot_mint - no self-replication).
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
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

func newAgentCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "agent",
		Short:   "Mint task identities, stream activity, wire hooks",
		GroupID: "agents",
		Long: `Agents are normal API clients with stricter defaults (§8.7): each task
gets its own ephemeral identity (create), reports what it is doing to
the workspace's live feed (event, hooks), and dies by TTL (revoke for
sooner).`,
		Example: `  runko agent create --task fix-rail-alignment
  runko agent hooks --install -w <workspace>   # live activity feed (§12.6.1)
  runko agent list`,
		Args: cobra.ArbitraryArgs,
		RunE: groupRunE,
	}
	cmd.AddCommand(newAgentCreateCmd(a), newAgentListCmd(a), newAgentRevokeCmd(a), newAgentEventCmd(a), newAgentHooksCmd())
	return cmd
}

func newAgentCreateCmd(a *app) *cobra.Command {
	var (
		task    string
		ttl     time.Duration
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "create --task <slug>",
		Short: "Mint an ephemeral task identity",
		Long: `Mints agent-<task>-<suffix> with a token shown exactly ONCE (default
TTL 8h, cap 168h). The harness or a human mints - an agent credential
is refused (agents_cannot_mint).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if task == "" {
				return usageError("usage: runko agent create --task <slug> [--ttl 8h]")
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			var out AgentIdentity
			err = apiJSON(cmd.Context(), http.DefaultClient, http.MethodPost,
				strings.TrimSuffix(cred.URL, "/")+"/api/agents", cred.AuthHeader(),
				map[string]any{"task": task, "ttl_seconds": int(ttl.Seconds())}, &out)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(out)
			}
			fmt.Printf("minted %s (task %s, expires %s)\n", out.Name, out.Task, out.ExpiresAt.Format(time.RFC3339))
			fmt.Printf("token (shown ONCE): %s\n", out.Token)
			fmt.Printf("use it:\n")
			fmt.Printf("  API/CLI:  --token %s:%s   (Basic name:token)\n", out.Name, out.Token)
			fmt.Printf("  git:      https://%s:%s@<host>/o/<org>/<org>.git\n", out.Name, out.Token)
			// The §12.6 golden-path teach (decided 2026-07-14): the streaming
			// commands, with the exact exports hooks need - hooks inherit an
			// environment, not flags (§12.6.1).
			// -w names the workspace this identity will work in, so the teach
			// stays runnable from the repo root (§12.7) - no cd, matching the
			// guidance `workspace create` prints once that name exists.
			fmt.Printf("stream the work (from your repo, once its workspace exists):\n")
			fmt.Printf("  export RUNKO_RUNKOD_URL=%s RUNKO_TOKEN=%s:%s\n", cred.URL, out.Name, out.Token)
			fmt.Printf("  runko workspace watch -w <workspace> &        # live WIP on the workspace page (§12.6)\n")
			fmt.Printf("  runko agent hooks --install -w <workspace>    # live activity feed (§12.6.1)\n")
			return nil
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&task, "task", "", "task slug the identity is minted for (lowercase, digits, dashes)")
	fl.DurationVar(&ttl, "ttl", 0, "credential lifetime (default 8h, capped at 168h)")
	fl.BoolVar(&jsonOut, "json", false, "emit the identity as JSON")
	return cmd
}

func newAgentListCmd(a *app) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Live and expired agent identities",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			var list []AgentIdentity
			if err := apiJSON(cmd.Context(), http.DefaultClient, http.MethodGet,
				strings.TrimSuffix(cred.URL, "/")+"/api/agents", cred.AuthHeader(), nil, &list); err != nil {
				return err
			}
			if jsonOut {
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
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the list as JSON")
	return cmd
}

func newAgentRevokeCmd(a *app) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "revoke <name>",
		Short: "Kill an agent credential immediately",
		Long: `Immediate credential kill; the row survives for attribution - a
revoked agent's landed work stays attributed to its name.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := requireArg(cmd, args, "agent name")
			if err != nil {
				return err
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			if err := apiJSON(cmd.Context(), http.DefaultClient, http.MethodPost,
				strings.TrimSuffix(cred.URL, "/")+"/api/agents/"+name+"/revoke", cred.AuthHeader(), map[string]string{}, nil); err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(map[string]string{"revoked": name})
			}
			fmt.Printf("revoked %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the result as JSON")
	return cmd
}
