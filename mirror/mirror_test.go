package mirror

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestAuthEnvComposition pins the two provider-agnosticism claims that are
// unit-testable: the only provider-specific atom is the basic-auth
// username (GitHub default, overridable for GitLab/Gitea/anything), and
// the token rides env-borne git config - NEVER argv, where `ps` would show
// it.
func TestAuthEnvComposition(t *testing.T) {
	cases := []struct {
		name     string
		remote   Remote
		wantUser string // "" = no auth env at all
	}{
		{"github default", Remote{URL: "https://github.com/o/r.git", Token: "tok"}, "x-access-token"},
		{"gitlab", Remote{URL: "https://gitlab.com/o/r.git", Token: "tok", Username: "oauth2"}, "oauth2"},
		{"gitea", Remote{URL: "https://gitea.example/o/r.git", Token: "tok", Username: "mirror-bot"}, "mirror-bot"},
		{"no token", Remote{URL: "https://github.com/o/r.git"}, ""},
		{"ssh remote ignores token", Remote{URL: "git@github.com:o/r.git", Token: "tok"}, ""},
		{"path remote ignores token", Remote{URL: "/srv/git/mirror.git", Token: "tok"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := tc.remote.env()
			if tc.wantUser == "" {
				if env != nil {
					t.Fatalf("want no auth env, got %v", env)
				}
				return
			}
			want := "GIT_CONFIG_VALUE_0=Authorization: Basic " +
				base64.StdEncoding.EncodeToString([]byte(tc.wantUser+":"+tc.remote.Token))
			found := false
			for _, kv := range env {
				if kv == want {
					found = true
				}
				if strings.Contains(kv, tc.remote.Token) && !strings.HasPrefix(kv, "GIT_CONFIG_VALUE_0=") {
					t.Fatalf("token leaked outside the header env var: %s", kv)
				}
			}
			if !found {
				t.Fatalf("auth header env missing: want %q in %v", want, env)
			}
		})
	}
}
