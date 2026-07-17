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
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
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

func cmdGithub(args []string) error {
	if len(args) < 1 {
		return usageError("usage: runko github connect|status ... (see docs/cli-contract.md)")
	}
	ctx := context.Background()
	switch args[0] {
	case "connect":
		fs := flag.NewFlagSet("github connect", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL (the /o/<org> mount)")
		token := fs.String("token", "", "deploy token")
		repo := fs.String("repo", "", "GitHub repo to wire this org to, owner/name")
		jsonOut := fs.Bool("json", false, "emit the wiring result as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *repo == "" {
			return usageError("usage: runko github connect --repo <owner/name>")
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		res, err := ConnectGithub(ctx, http.DefaultClient, cred, *repo)
		if err != nil {
			return err
		}
		if *jsonOut {
			return json.NewEncoder(os.Stdout).Encode(res)
		}
		fmt.Printf("wired org %s to %s\n", res.Org, res.RemoteURL)
		fmt.Println("  verified: repo reachable, GitHub App installed, push token minted")
		fmt.Println("  mirror:   armed; first sync triggered (runko github status)")
		fmt.Println("  dispatch: native (2026-07-17) - the outbox sends repository_dispatch for this org's changes itself")
		fmt.Println("one manual step remains, on the GitHub repo:")
		fmt.Println("  workflow: .github/workflows/runko-checks.yml (repository_dispatch types: [runko-change] -> runko-ci checks)")
		return nil

	case "status":
		fs := flag.NewFlagSet("github status", flag.ExitOnError)
		runkodURL := fs.String("runkod-url", "", "runkod base URL (the /o/<org> mount)")
		token := fs.String("token", "", "deploy token")
		jsonOut := fs.Bool("json", false, "emit the mirror status as JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		cred, err := resolveCredential(*runkodURL, *token)
		if err != nil {
			return err
		}
		status, err := GithubMirrorStatus(ctx, http.DefaultClient, cred)
		if err != nil {
			return err
		}
		if *jsonOut {
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
	}
	return usageError("usage: runko github connect|status ...")
}
