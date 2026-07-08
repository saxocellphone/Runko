// runko auth: persistent CLI sign-in (§17.1's `runko auth login`, the last
// §19.2 stub). One stored credential per user (~/.config/runko/
// credentials.json, 0600 - the gh/netrc convention): either a named
// principal (name + password -> HTTP Basic, works for signed-up principals
// whose passwords are hashed server-side and can never be bearer tokens)
// or a bare token (-> Bearer: deploy token, operator principal, bot lane).
// Every networked command resolves flags > stored credential, so after one
// `runko auth login` the flags disappear from daily use.
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/saxocellphone/runko/internal/clierr"
)

// authHeaderValue accepts either a raw bearer token (the historical
// --token flag form; tests and scripts pass these) or a pre-rendered
// Authorization value from the stored login ("Bearer x" / "Basic y",
// auth.go) and returns the header to send.
func authHeaderValue(tokenOrHeader string) string {
	if strings.HasPrefix(tokenOrHeader, "Bearer ") || strings.HasPrefix(tokenOrHeader, "Basic ") {
		return tokenOrHeader
	}
	return "Bearer " + tokenOrHeader
}

// gitUserPass converts a token-or-header (authHeaderValue's input form)
// into the smart-HTTP Basic pair for a git remote URL.
func gitUserPass(tokenOrHeader string) (user, pass string) {
	if b64, found := strings.CutPrefix(tokenOrHeader, "Basic "); found {
		if raw, err := base64.StdEncoding.DecodeString(b64); err == nil {
			if u, p, ok := strings.Cut(string(raw), ":"); ok {
				return u, p
			}
		}
	}
	return "runko", strings.TrimPrefix(tokenOrHeader, "Bearer ")
}

// Credential is what `auth login` stores and every command resolves.
type Credential struct {
	URL    string `json:"url"`
	Name   string `json:"name,omitempty"` // empty -> Secret is a bearer token
	Secret string `json:"secret"`
}

// AuthHeader renders the Authorization header value runkod's resolver
// (runkod/auth.go) expects for this credential form.
func (c Credential) AuthHeader() string {
	if c.Name != "" {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.Name+":"+c.Secret))
	}
	return "Bearer " + c.Secret
}

// GitUserPass is the smart-HTTP Basic pair for embedding in a git remote
// URL; the "runko" username is the documented anonymous-deploy-token form.
func (c Credential) GitUserPass() (user, pass string) {
	if c.Name != "" {
		return c.Name, c.Secret
	}
	return "runko", c.Secret
}

// credentialPath honors XDG_CONFIG_HOME explicitly on EVERY platform, then
// falls back to os.UserConfigDir(). Go's UserConfigDir ignores XDG on
// macOS (~/Library/Application Support), which made the env var a silent
// no-op there: the test sandbox leaked through to the real credential file,
// and docs/cli-contract.md's "~/.config/runko/" promise only held on Linux.
func credentialPath() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "runko", "credentials.json"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runko", "credentials.json"), nil
}

func saveCredential(c Credential) (string, error) {
	path, err := credentialPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(b, '\n'), 0o600)
}

func loadCredential() (Credential, bool, error) {
	path, err := credentialPath()
	if err != nil {
		return Credential{}, false, err
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Credential{}, false, nil
	}
	if err != nil {
		return Credential{}, false, err
	}
	var c Credential
	if err := json.Unmarshal(b, &c); err != nil {
		return Credential{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return c, true, nil
}

// resolveCredential is every networked command's auth source: explicit
// flags win (the pre-login scripting form, always Bearer), then the stored
// login; anything else is a structured "log in first".
func resolveCredential(urlFlag, tokenFlag string) (Credential, error) {
	if tokenFlag != "" {
		if urlFlag == "" {
			return Credential{}, &clierr.Error{
				Code: "missing_url", Field: "runkod-url",
				Message:    "--token was given without --runkod-url",
				Suggestion: "pass both flags, or store them once with `runko auth login`",
			}
		}
		return Credential{URL: urlFlag, Secret: tokenFlag}, nil
	}
	cred, found, err := loadCredential()
	if err != nil {
		return Credential{}, err
	}
	if !found {
		return Credential{}, &clierr.Error{
			Code: "not_logged_in", Field: "auth",
			Message:    "no credential: pass --runkod-url/--token, or log in once",
			Suggestion: "runko auth login --runkod-url <url> [--name <you>]",
		}
	}
	if urlFlag != "" {
		cred.URL = urlFlag
	}
	return cred, nil
}

// whoami validates a credential against the control plane and reports the
// resolved identity ("" = the anonymous deploy token).
func whoami(ctx context.Context, client *http.Client, cred Credential) (name string, anonymous bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimSuffix(cred.URL, "/")+"/api/whoami", nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", cred.AuthHeader())
	resp, err := client.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", false, &clierr.Error{
			Code: "bad_credential", Field: "auth",
			Message:    "the control plane rejected this credential",
			Suggestion: "check the name/password (or token) and try again",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("whoami: HTTP %d", resp.StatusCode)
	}
	var body struct {
		Name      string `json:"name"`
		Anonymous bool   `json:"anonymous"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", false, err
	}
	return body.Name, body.Anonymous, nil
}

// AuthLogin validates and stores a credential. secret == "" prompts on
// stdin (plainly - no terminal-mode dependency; fine for a homelab CLI,
// and scripts pass --token/--password anyway).
func AuthLogin(ctx context.Context, client *http.Client, url, name, secret string, prompt *bufio.Reader, out *os.File) (Credential, error) {
	if secret == "" {
		label := "token"
		if name != "" {
			label = "password for " + name
		}
		fmt.Fprintf(out, "%s (input is echoed): ", label)
		line, err := prompt.ReadString('\n')
		if err != nil {
			return Credential{}, fmt.Errorf("read secret: %w", err)
		}
		secret = strings.TrimSpace(line)
	}
	cred := Credential{URL: strings.TrimSuffix(url, "/"), Name: name, Secret: secret}
	who, anonymous, err := whoami(ctx, client, cred)
	if err != nil {
		return Credential{}, err
	}
	path, err := saveCredential(cred)
	if err != nil {
		return Credential{}, err
	}
	switch {
	case anonymous:
		fmt.Fprintf(out, "logged in to %s anonymously (deploy token); stored in %s\n", cred.URL, path)
	default:
		fmt.Fprintf(out, "logged in to %s as %s; stored in %s\n", cred.URL, who, path)
	}
	return cred, nil
}
