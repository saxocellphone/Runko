// Native Mode C dispatch (2026-07-17, runkod/README.md): the runko-bridge
// shim folded into the daemon. When an org's settings carry GitHub wiring
// (github_mirror_repo, written by `runko github connect`), the outbox's
// change.updated / change.check_rerun_requested envelopes become GitHub
// repository_dispatch events minted with the deployment's GitHub App - no
// per-org bridge deployment, no webhook hop. The bridge binary remains
// the shim for deployments without App credentials.
//
// Delivery semantics are the outbox's (§14.4.1): one attempt per due row,
// exponential backoff, dead-letter after MaxDeliveryAttempts. A duplicate
// dispatch after a partial failure is deduped GitHub-side by the
// workflow's concurrency group (change_id + head_sha) - the same
// restart-safety the bridge documented.
package runkod

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/saxocellphone/runko/platform/checks"
)

// githubDispatchEventType is the repository_dispatch event_type the
// runko-checks workflow subscribes to (types: [runko-change]) - the
// bridge's --event-type default, now fixed: the workflow template and the
// daemon are one product surface.
const githubDispatchEventType = "runko-change"

// GithubDispatcher turns webhook envelopes into repository_dispatch
// calls for one org. Zero value is unusable; cmd/runkod wires it only
// when the deployment holds GitHub App credentials.
type GithubDispatcher struct {
	// Directory + OrgName resolve the org's stored GitHub wiring per
	// delivery - an org connected mid-flight starts dispatching without
	// any restart, and an unwired org's deliveries succeed as no-ops.
	Directory Directory
	OrgName   string
	// Token mints an installation token for "owner/name" (the App
	// client's Token method; cached and refreshed there).
	Token func(ctx context.Context, repo string) (string, error)
	// APIBase is https://api.github.com (or the GHES /api/v3 base).
	APIBase string
	Client  *http.Client
}

// dispatchPayload is the repository_dispatch client_payload - the wire
// contract the runko-checks workflow reads (GitHub caps client_payload at
// 10 top-level keys). Field-for-field the bridge's shape: the workflow
// must not notice the shim being replaced.
type dispatchPayload struct {
	ChangeID         string   `json:"change_id"`
	HeadSHA          string   `json:"head_sha"`
	BaseSHA          string   `json:"base_sha"`
	GitRef           string   `json:"git_ref"`
	Trigger          string   `json:"trigger"`
	RerunCheck       string   `json:"rerun_check,omitempty"`
	DeliveryID       string   `json:"delivery_id"`
	AffectedProjects []string `json:"affected_projects"`
}

// Deliver attempts one dispatch for one outbox payload. Envelope types
// CI never runs on, and orgs with no GitHub wiring, succeed as no-ops -
// the outbox marks them delivered instead of retrying forever.
func (g *GithubDispatcher) Deliver(ctx context.Context, payload []byte) checks.DeliveryAttempt {
	var env checks.WebhookEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		// A payload the daemon itself wrote failing to parse is a bug,
		// not a transient - don't burn retries on it.
		return checks.DeliveryAttempt{Success: true, Err: nil}
	}
	if env.Type != "change.updated" && env.Type != "change.check_rerun_requested" {
		return checks.DeliveryAttempt{Success: true}
	}
	settings, err := g.Directory.GetOrgSettings(ctx, g.OrgName)
	if err != nil {
		return checks.DeliveryAttempt{Err: fmt.Errorf("github dispatch: org settings: %w", err)}
	}
	if settings.GithubMirrorRepo == "" {
		return checks.DeliveryAttempt{Success: true} // org not wired to GitHub
	}

	token, err := g.Token(ctx, settings.GithubMirrorRepo)
	if err != nil {
		return checks.DeliveryAttempt{Err: fmt.Errorf("github dispatch: mint token: %w", err)}
	}

	dp := dispatchPayload{
		ChangeID:   env.Change.ID,
		HeadSHA:    env.Change.HeadSHA,
		BaseSHA:    env.Change.BaseSHA,
		GitRef:     env.Change.GitRef,
		Trigger:    env.Type,
		DeliveryID: env.DeliveryID,
	}
	if env.Rerun != nil {
		dp.RerunCheck = env.Rerun.CheckName
	}
	if env.Affected != nil {
		for _, p := range env.Affected.Projects {
			dp.AffectedProjects = append(dp.AffectedProjects, p.Name)
		}
	}
	body, err := json.Marshal(map[string]any{"event_type": githubDispatchEventType, "client_payload": dp})
	if err != nil {
		return checks.DeliveryAttempt{Err: fmt.Errorf("github dispatch: marshal: %w", err)}
	}

	url := strings.TrimRight(g.APIBase, "/") + "/repos/" + settings.GithubMirrorRepo + "/dispatches"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return checks.DeliveryAttempt{Err: fmt.Errorf("github dispatch: build request: %w", err)}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := g.client().Do(req)
	if err != nil {
		return checks.DeliveryAttempt{Err: fmt.Errorf("github dispatch: %w", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return checks.DeliveryAttempt{StatusCode: resp.StatusCode,
			Err: fmt.Errorf("github dispatch: github returned %d: %s", resp.StatusCode, msg)}
	}
	return checks.DeliveryAttempt{Success: true, StatusCode: resp.StatusCode}
}

func (g *GithubDispatcher) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return http.DefaultClient
}
