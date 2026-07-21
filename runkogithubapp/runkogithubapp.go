// Package runkogithubapp mints GitHub App installation tokens - the
// deployment-wide replacement for per-org PATs on the GitHub integration
// plane (§14.3 Mode C dispatch via runko-bridge, §18.6 M1 mirror pushes).
// One App credential (app id + RS256 private key) serves every org: an
// org's GitHub setup shrinks to installing the App on its mirror repo,
// and short-lived installation tokens are minted on demand and cached
// until shortly before their one-hour expiry.
//
// An installation token is a drop-in PAT: Bearer auth for REST
// (repository_dispatch) and the "x-access-token" basic-auth password for
// git-over-https. platform/mirror stays provider-agnostic by
// construction - it receives a plain TokenSource func, never this
// package.
//
// Implemented on stdlib crypto only (RS256 = RSASSA-PKCS1-v1_5 over
// SHA-256); no new dependencies.
package runkogithubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// refreshMargin is how long before expiry a cached token stops being
// served: generous enough that a token handed out stays valid across a
// whole git push or REST call.
const refreshMargin = 5 * time.Minute

// App is one GitHub App credential, safe for concurrent use. All methods
// key repos as "owner/name".
type App struct {
	appID   string
	key     *rsa.PrivateKey
	apiBase string
	client  *http.Client
	now     func() time.Time

	mu       sync.Mutex
	tokens   map[string]instToken // owner/name -> live installation token
	installs map[string]int64     // owner/name -> installation id
}

type instToken struct {
	value     string
	expiresAt time.Time
}

// New parses the App's private key PEM (PKCS#1 as GitHub issues it, or
// PKCS#8) and returns a minting client. apiBase is
// "https://api.github.com" for github.com, "https://<host>/api/v3" for
// GHES.
func New(appID string, keyPEM []byte, apiBase string) (*App, error) {
	if appID == "" {
		return nil, fmt.Errorf("githubapp: app id is required")
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	return &App{
		appID:    appID,
		key:      key,
		apiBase:  strings.TrimRight(apiBase, "/"),
		client:   &http.Client{Timeout: 30 * time.Second},
		now:      time.Now,
		tokens:   make(map[string]instToken),
		installs: make(map[string]int64),
	}, nil
}

func parseKey(keyPEM []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("githubapp: private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("githubapp: parse private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("githubapp: private key is %T, GitHub Apps need RSA", parsed)
	}
	return key, nil
}

// Token returns an installation token for the repo, minting or
// refreshing through the App JWT as needed.
func (a *App) Token(ctx context.Context, ownerRepo string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.tokens[ownerRepo]; ok && a.now().Add(refreshMargin).Before(t.expiresAt) {
		return t.value, nil
	}
	t, err := a.mint(ctx, ownerRepo)
	if err != nil {
		return "", err
	}
	a.tokens[ownerRepo] = t
	return t.value, nil
}

// TokenSource adapts Token to the plain func shape provider-agnostic
// callers (mirror.Remote) accept.
func (a *App) TokenSource(ownerRepo string) func() (string, error) {
	return func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return a.Token(ctx, ownerRepo)
	}
}

// RepoPath returns "owner/name" when remoteURL is an https remote on
// this App's GitHub host ("" otherwise): github.com for the public API
// base, the API host itself for GHES. This is the provider-detection
// atom callers use to decide whether App auth applies to a remote - a
// non-matching remote keeps whatever auth it was configured with.
func (a *App) RepoPath(remoteURL string) string {
	u, err := url.Parse(remoteURL)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	host := "github.com"
	if api, err := url.Parse(a.apiBase); err == nil && api.Host != "" && api.Host != "api.github.com" {
		host = api.Host
	}
	if u.Host != host {
		return ""
	}
	path := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
	if owner, name, ok := strings.Cut(path, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return ""
	}
	return path
}

// mint exchanges the App JWT for an installation token (caller holds
// a.mu). A cached installation id that has gone stale (App reinstalled
// since we resolved it) is re-resolved exactly once.
func (a *App) mint(ctx context.Context, ownerRepo string) (instToken, error) {
	id, cached := a.installs[ownerRepo]
	if !cached {
		fresh, err := a.lookupInstallation(ctx, ownerRepo)
		if err != nil {
			return instToken{}, err
		}
		id, a.installs[ownerRepo] = fresh, fresh
	}
	t, status, err := a.createToken(ctx, id, ownerRepo)
	if status == http.StatusNotFound && cached {
		delete(a.installs, ownerRepo)
		fresh, lerr := a.lookupInstallation(ctx, ownerRepo)
		if lerr != nil {
			return instToken{}, lerr
		}
		a.installs[ownerRepo] = fresh
		t, _, err = a.createToken(ctx, fresh, ownerRepo)
	}
	return t, err
}

func (a *App) lookupInstallation(ctx context.Context, ownerRepo string) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	status, err := a.call(ctx, http.MethodGet, "/repos/"+ownerRepo+"/installation", nil, &out)
	if err != nil {
		return 0, fmt.Errorf("githubapp: resolve installation for %s: %w", ownerRepo, err)
	}
	if status == http.StatusNotFound {
		return 0, fmt.Errorf("githubapp: the GitHub App is not installed on %s - install it on that repo (GitHub -> Settings -> GitHub Apps) and retry", ownerRepo)
	}
	if status != http.StatusOK {
		return 0, fmt.Errorf("githubapp: resolve installation for %s: github returned %d", ownerRepo, status)
	}
	return out.ID, nil
}

// createToken mints scoped DOWN to the one repo it was asked for: the
// "repositories" mint body narrows the token below the installation's
// full repo selection, so a token minted for one repo can never write a
// sibling repo sharing the same installation (one user account = one
// installation covering every selected repo).
func (a *App) createToken(ctx context.Context, installationID int64, ownerRepo string) (instToken, int, error) {
	var out struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	in := map[string]any{}
	if _, name, ok := strings.Cut(ownerRepo, "/"); ok && name != "" {
		in["repositories"] = []string{name}
	}
	path := fmt.Sprintf("/app/installations/%d/access_tokens", installationID)
	status, err := a.call(ctx, http.MethodPost, path, in, &out)
	if err != nil {
		return instToken{}, 0, fmt.Errorf("githubapp: mint installation token: %w", err)
	}
	if status != http.StatusCreated {
		return instToken{}, status, fmt.Errorf("githubapp: mint installation token: github returned %d", status)
	}
	return instToken{value: out.Token, expiresAt: out.ExpiresAt}, status, nil
}

// call performs one JWT-authenticated App API request (in != nil is
// sent as a JSON body), decoding a 2xx body into out and reporting
// every other status to the caller.
func (a *App) call(ctx context.Context, method, path string, in, out any) (int, error) {
	jwt, err := a.jwt()
	if err != nil {
		return 0, err
	}
	var body io.Reader
	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.apiBase+path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+jwt)
	resp, err := a.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode github response: %w", err)
		}
	}
	return resp.StatusCode, nil
}

// jwt signs the short-lived App JWT GitHub's App endpoints authenticate
// with: RS256, issued-at backdated 60s against clock skew, 9-minute
// expiry (GitHub caps at 10).
func (a *App) jwt() (string, error) {
	claims, err := json.Marshal(map[string]any{
		"iat": a.now().Add(-time.Minute).Unix(),
		"exp": a.now().Add(9 * time.Minute).Unix(),
		"iss": a.appID,
	})
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding
	signing := b64.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("githubapp: sign app jwt: %w", err)
	}
	return signing + "." + b64.EncodeToString(sig), nil
}
