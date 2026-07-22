// GitHub wiring verbs (2026-07-16): `runko github
// connect` turns "wire this org to GitHub" into one command against the
// org's POST /api/github/connect - the server verifies the deployment's
// GitHub App can push to the repo, persists the wiring in org settings,
// and arms the mirror worker live. `runko github status` renders the
// org's mirror state (GET /api/mirror/status). Org-scoped like every
// other verb: they act on whatever org the credential points at.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// GithubConnectResult mirrors POST /api/github/connect's wire shape.
type GithubConnectResult struct {
	Org       string `json:"org"`
	Repo      string `json:"repo"`
	RemoteURL string `json:"remote_url"`
	Mirror    string `json:"mirror"`
}

// MirrorStatus mirrors GET /api/mirror/status (runkod's mirrorStatus -
// untagged struct, so the wire keys are the Go field names).
type MirrorStatus struct {
	Configured bool
	RemoteURL  string
	Cursors    []struct {
		Ref           string
		LastSyncedSHA string
		Frozen        bool
		UpdatedAt     time.Time
	}
	LastError  string
	LastSyncAt time.Time
}

func ConnectGithub(ctx context.Context, client *http.Client, cred Credential, repo string) (GithubConnectResult, error) {
	var out GithubConnectResult
	err := apiJSON(ctx, client, http.MethodPost,
		strings.TrimSuffix(cred.URL, "/")+"/api/github/connect", cred.AuthHeader(),
		map[string]string{"repo": repo}, &out)
	return out, err
}

func GithubMirrorStatus(ctx context.Context, client *http.Client, cred Credential) (MirrorStatus, error) {
	var out MirrorStatus
	err := apiJSON(ctx, client, http.MethodGet,
		strings.TrimSuffix(cred.URL, "/")+"/api/mirror/status", cred.AuthHeader(), nil, &out)
	return out, err
}

func newGithubCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "github",
		Short:   "Wire and inspect the outbound GitHub mirror",
		GroupID: "repo",
		Args:    cobra.ArbitraryArgs,
		RunE:    groupRunE,
	}
	cmd.AddCommand(newGithubConnectCmd(a), newGithubStatusCmd(a))
	return cmd
}

func newGithubConnectCmd(a *app) *cobra.Command {
	var (
		repo    string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "connect --repo <owner/name>",
		Short: "Wire this org to a GitHub repo in one command",
		Long: `One call wires the org to GitHub (2026-07-16): the server verifies its
deployment-wide GitHub App can actually push, persists the wiring in
org settings, and arms the mirror worker live - no daemon restart.
Covers CI dispatch too (the outbox sends repository_dispatch itself).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if repo == "" {
				return usageError("usage: runko github connect --repo <owner/name>")
			}
			cred, err := a.credential()
			if err != nil {
				return err
			}
			res, err := ConnectGithub(cmd.Context(), http.DefaultClient, cred, repo)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(res)
			}
			fmt.Printf("wired org %s to %s\n", res.Org, res.RemoteURL)
			fmt.Println("  verified: repo reachable, GitHub App installed, push token minted")
			fmt.Println("  mirror:   armed; first sync triggered (runko github status)")
			fmt.Println("  dispatch: native (2026-07-17) - the outbox sends repository_dispatch for this org's changes itself")
			fmt.Println("one manual step remains, on the GitHub repo:")
			fmt.Println("  workflow: .github/workflows/runko-checks.yml (repository_dispatch types: [runko-change] -> runko-ci checks)")
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repo to wire this org to, owner/name")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the wiring result as JSON")
	return cmd
}

func newGithubStatusCmd(a *app) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "The org's mirror state: target, cursors, freezes",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cred, err := a.credential()
			if err != nil {
				return err
			}
			status, err := GithubMirrorStatus(cmd.Context(), http.DefaultClient, cred)
			if err != nil {
				return err
			}
			if jsonOut {
				return json.NewEncoder(os.Stdout).Encode(status)
			}
			if !status.Configured {
				fmt.Println("no mirror configured")
				fmt.Println("  -> runko github connect --repo <owner/name>   # wire this org to GitHub")
				return nil
			}
			fmt.Printf("mirroring to %s\n", status.RemoteURL)
			if !status.LastSyncAt.IsZero() {
				fmt.Printf("  last sync: %s\n", status.LastSyncAt.Format(time.RFC3339))
			}
			if status.LastError != "" {
				fmt.Printf("  last error: %s\n", status.LastError)
			}
			for _, c := range status.Cursors {
				state := "ok"
				if c.Frozen {
					state = "FROZEN (POST /api/mirror/unfreeze after review)"
				}
				sha := c.LastSyncedSHA
				if len(sha) > 12 {
					sha = sha[:12]
				}
				fmt.Printf("  %-20s %-12s %s\n", c.Ref, sha, state)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the mirror status as JSON")
	return cmd
}
