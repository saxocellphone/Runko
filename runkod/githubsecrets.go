// GitHub Actions CI credential provisioning (2026-07-29). `runko github
// connect` armed the OUTBOUND half of CI - mirror push and
// repository_dispatch - and left the runner unable to answer: the
// runko-checks workflow reports results back with RUNKO_URL and
// RUNKO_CI_TOKEN, and nothing set them. A fresh monorepo therefore
// bootstrapped into a state where every dispatch ran and every check
// failed at `report in_progress` with an empty URL, which reads as
// "checks hang forever" from the merge gate's side (dogfood, 2026-07-29).
//
// So connect finishes the job. Two writes, deliberately different kinds:
//
//   - RUNKO_URL is a repo VARIABLE. It is the org's own mount URL - public,
//     already in every clone's remote - so encrypting it would buy nothing
//     and cost the sealed-box round trip.
//   - RUNKO_CI_TOKEN is a repo SECRET, which GitHub requires be encrypted
//     CLIENT-side with a NaCl sealed box (crypto_box_seal) against a
//     per-repo public key. That is the one thing here Go's standard library
//     cannot do: crypto/ecdh has X25519 but no XSalsa20-Poly1305, so
//     golang.org/x/crypto/nacl/box carries it (sanctioned 2026-07-29).
//
// The token value is never derived or invented here - the caller supplies
// it. A deployment-wide deploy token planted in every mirror repo would let
// any repo holding it act on any org, so the narrower credential is the
// caller's choice to make, not this endpoint's to guess.
package runkod

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/crypto/nacl/box"
)

// githubActionsSecretName / githubActionsURLVariable are the two names the
// runko-checks workflow template reads (templates/ci/runko-checks.yml's
// env block). They are a contract between that template and this code:
// renaming one without the other silently breaks reporting.
const (
	githubActionsURLVariable = "RUNKO_URL"
	githubActionsSecretName  = "RUNKO_CI_TOKEN"
)

// ciProvisionResult reports what connect actually wrote, so the CLI can say
// so instead of claiming success for a step it skipped.
type ciProvisionResult struct {
	Variable string `json:"variable,omitempty"`
	Secret   string `json:"secret,omitempty"`
	Skipped  string `json:"skipped,omitempty"`
}

// githubAPI performs one installation-token-authenticated REST call against
// the repo, decoding a 2xx body into out when out != nil. It returns the
// status alongside the error so callers can tell "no permission" (403) from
// "no such repo" (404) and say something useful about each.
func (s *Server) githubAPI(ctx context.Context, method, repo, path string, in, out any) (int, error) {
	if s.GithubToken == nil || s.GithubAPIBase == "" {
		return 0, fmt.Errorf("githubsecrets: server holds no GitHub API wiring")
	}
	token, err := s.GithubToken(ctx, repo)
	if err != nil {
		return 0, fmt.Errorf("githubsecrets: mint installation token: %w", err)
	}
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = strings.NewReader(string(payload))
	}
	url := strings.TrimSuffix(s.GithubAPIBase, "/") + "/repos/" + repo + path
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := s.GithubClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// Read a bounded prefix: GitHub's error bodies are small and name
		// the missing permission, which is the whole diagnosis here.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return resp.StatusCode, fmt.Errorf("github returned %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// sealSecret encrypts plaintext for GitHub with crypto_box_seal against the
// repo's base64 X25519 public key, returning the base64 ciphertext GitHub's
// PUT expects. box.SealAnonymous generates the ephemeral keypair itself, so
// the same plaintext seals differently every call - that is the primitive
// working, not nondeterminism to fix.
func sealSecret(publicKeyB64, plaintext string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return "", fmt.Errorf("githubsecrets: decode repo public key: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("githubsecrets: repo public key is %d bytes, want 32", len(raw))
	}
	var key [32]byte
	copy(key[:], raw)
	sealed, err := box.SealAnonymous(nil, []byte(plaintext), &key, rand.Reader)
	if err != nil {
		return "", fmt.Errorf("githubsecrets: seal: %w", err)
	}
	return base64.StdEncoding.EncodeToString(sealed), nil
}

// putRepoVariable creates or updates one Actions variable. GitHub has no
// upsert here: POST creates and 409s when it exists, PATCH updates and 404s
// when it does not, so a re-connect must fall through from one to the other.
func (s *Server) putRepoVariable(ctx context.Context, repo, name, value string) error {
	status, err := s.githubAPI(ctx, http.MethodPost, repo, "/actions/variables",
		map[string]string{"name": name, "value": value}, nil)
	if err == nil {
		return nil
	}
	if status != http.StatusConflict {
		return err
	}
	if _, err := s.githubAPI(ctx, http.MethodPatch, repo, "/actions/variables/"+name,
		map[string]string{"name": name, "value": value}, nil); err != nil {
		return err
	}
	return nil
}

// putRepoSecret seals value against the repo's public key and PUTs it.
// PUT is a true upsert for secrets (201 created / 204 updated), so unlike
// variables this needs no fallback.
func (s *Server) putRepoSecret(ctx context.Context, repo, name, value string) error {
	var pk struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}
	if _, err := s.githubAPI(ctx, http.MethodGet, repo, "/actions/secrets/public-key", nil, &pk); err != nil {
		return err
	}
	sealed, err := sealSecret(pk.Key, value)
	if err != nil {
		return err
	}
	_, err = s.githubAPI(ctx, http.MethodPut, repo, "/actions/secrets/"+name,
		map[string]string{"encrypted_value": sealed, "key_id": pk.KeyID}, nil)
	return err
}

// provisionCI writes the two values the runko-checks workflow needs. The
// URL always goes (it is derivable and harmless); the token only when the
// caller supplied one, and its absence is REPORTED rather than silently
// treated as success - the failure this whole file exists to prevent was an
// unset secret that nothing complained about.
func (s *Server) provisionCI(ctx context.Context, repo, orgURL, ciToken string) (ciProvisionResult, error) {
	var out ciProvisionResult
	if s.GithubToken == nil || s.GithubAPIBase == "" {
		out.Skipped = "this deployment holds no GitHub API wiring, so CI values were not written"
		return out, nil
	}
	if orgURL != "" {
		if err := s.putRepoVariable(ctx, repo, githubActionsURLVariable, orgURL); err != nil {
			return out, err
		}
		out.Variable = githubActionsURLVariable
	}
	if ciToken == "" {
		out.Skipped = githubActionsSecretName + " not set (no --ci-token given); checks cannot report until it is"
		return out, nil
	}
	if err := s.putRepoSecret(ctx, repo, githubActionsSecretName, ciToken); err != nil {
		return out, err
	}
	out.Secret = githubActionsSecretName
	return out, nil
}
