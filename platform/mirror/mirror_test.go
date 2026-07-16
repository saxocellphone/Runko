package mirror

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// TestAuthEnvComposition pins the two provider-agnosticism claims that are
// unit-testable: the only provider-specific atom is the basic-auth
// username (GitHub default, overridable for GitLab/Gitea/anything), and
// the token rides env-borne git config - NEVER argv, where `ps` would show
// it.
func TestAuthEnvComposition(t *testing.T) {
	sourced := func(tok string) func() (string, error) {
		return func() (string, error) { return tok, nil }
	}
	cases := []struct {
		name      string
		remote    Remote
		wantUser  string // "" = no auth env at all
		wantToken string
	}{
		{"github default", Remote{URL: "https://github.com/o/r.git", Token: "tok"}, "x-access-token", "tok"},
		{"gitlab", Remote{URL: "https://gitlab.com/o/r.git", Token: "tok", Username: "oauth2"}, "oauth2", "tok"},
		{"gitea", Remote{URL: "https://gitea.example/o/r.git", Token: "tok", Username: "mirror-bot"}, "mirror-bot", "tok"},
		{"no token", Remote{URL: "https://github.com/o/r.git"}, "", ""},
		{"ssh remote ignores token", Remote{URL: "git@github.com:o/r.git", Token: "tok"}, "", ""},
		{"path remote ignores token", Remote{URL: "/srv/git/mirror.git", Token: "tok"}, "", ""},
		{"token source minted per call", Remote{URL: "https://github.com/o/r.git", TokenSource: sourced("ghs_minted")}, "x-access-token", "ghs_minted"},
		{"token source wins over static", Remote{URL: "https://github.com/o/r.git", Token: "stale", TokenSource: sourced("ghs_fresh")}, "x-access-token", "ghs_fresh"},
		{"empty token source result means no auth", Remote{URL: "https://github.com/o/r.git", TokenSource: sourced("")}, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := tc.remote.env()
			if err != nil {
				t.Fatalf("env: %v", err)
			}
			if tc.wantUser == "" {
				if env != nil {
					t.Fatalf("want no auth env, got %v", env)
				}
				return
			}
			want := "GIT_CONFIG_VALUE_0=Authorization: Basic " +
				base64.StdEncoding.EncodeToString([]byte(tc.wantUser+":"+tc.wantToken))
			found := false
			for _, kv := range env {
				if kv == want {
					found = true
				}
				if strings.Contains(kv, tc.wantToken) && !strings.HasPrefix(kv, "GIT_CONFIG_VALUE_0=") {
					t.Fatalf("token leaked outside the header env var: %s", kv)
				}
			}
			if !found {
				t.Fatalf("auth header env missing: want %q in %v", want, env)
			}
		})
	}
}

// TestTokenSourceFailureFailsTheInvocation pins the retry contract: a
// failing token mint fails the one git call (the worker's debounce +
// reconcile loop re-drives it) instead of silently pushing unauthed.
func TestTokenSourceFailureFailsTheInvocation(t *testing.T) {
	r := Remote{
		RepoDir: t.TempDir(),
		URL:     "https://github.com/o/r.git",
		TokenSource: func() (string, error) {
			return "", errors.New("mint failed")
		},
	}
	if _, err := r.LsRemote("refs/heads/main"); err == nil || !strings.Contains(err.Error(), "mint failed") {
		t.Fatalf("want the token-source failure surfaced, got %v", err)
	}
}
