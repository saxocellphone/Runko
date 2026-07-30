package runkod

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

// fakeGithub is a stand-in for the Actions variables/secrets API. It holds a
// REAL X25519 keypair, so the secret this code seals must actually open with
// the matching private key - the assertion is decryption, not a recorded
// call.
type fakeGithub struct {
	pub, priv *[32]byte
	variables map[string]string
	secrets   map[string]string // name -> sealed base64
	// variableExists forces the POST path to 409, exercising the
	// create-then-update fallback a re-connect takes.
	variableExists bool
	patched        bool
	tokenSeen      string
}

func newFakeGithub(t *testing.T) (*fakeGithub, *httptest.Server) {
	t.Helper()
	pub, priv, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	f := &fakeGithub{pub: pub, priv: priv,
		variables: map[string]string{}, secrets: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.tokenSeen = r.Header.Get("Authorization")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/actions/secrets/public-key"):
			json.NewEncoder(w).Encode(map[string]string{
				"key_id": "kid-1",
				"key":    base64.StdEncoding.EncodeToString(f.pub[:]),
			})
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/actions/secrets/"):
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			if in["key_id"] != "kid-1" {
				t.Errorf("secret PUT carried key_id %q, want kid-1", in["key_id"])
			}
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			f.secrets[name] = in["encrypted_value"]
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/actions/variables"):
			if f.variableExists {
				w.WriteHeader(http.StatusConflict)
				return
			}
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			f.variables[in["name"]] = in["value"]
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/actions/variables/"):
			var in map[string]string
			json.NewDecoder(r.Body).Decode(&in)
			f.patched = true
			f.variables[in["name"]] = in["value"]
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return f, srv
}

// open decrypts what the server sealed, proving the sealed box round-trips.
func (f *fakeGithub) open(t *testing.T, name string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(f.secrets[name])
	if err != nil {
		t.Fatalf("secret %s is not base64: %v", name, err)
	}
	out, ok := box.OpenAnonymous(nil, raw, f.pub, f.priv)
	if !ok {
		t.Fatalf("secret %s did not open with the repo keypair", name)
	}
	return string(out)
}

func serverFor(srv *httptest.Server) *Server {
	return &Server{
		GithubAPIBase: srv.URL,
		GithubClient:  srv.Client(),
		GithubToken: func(ctx context.Context, repo string) (string, error) {
			return "inst-token-for-" + repo, nil
		},
	}
}

func TestProvisionCIWritesVariableAndSealedSecret(t *testing.T) {
	f, srv := newFakeGithub(t)
	defer srv.Close()
	s := serverFor(srv)

	res, err := s.provisionCI(context.Background(), "acme/monorepo",
		"https://runko.example.com/o/acme", "deploy-tok")
	if err != nil {
		t.Fatalf("provisionCI: %v", err)
	}
	if res.Variable != "RUNKO_URL" || res.Secret != "RUNKO_CI_TOKEN" {
		t.Fatalf("result: %+v", res)
	}
	if got := f.variables["RUNKO_URL"]; got != "https://runko.example.com/o/acme" {
		t.Fatalf("RUNKO_URL = %q", got)
	}
	// The whole point: the runner must be able to decrypt this.
	if got := f.open(t, "RUNKO_CI_TOKEN"); got != "deploy-tok" {
		t.Fatalf("sealed secret opened to %q, want deploy-tok", got)
	}
	// The write must use a repo-scoped installation token, not the App JWT.
	if !strings.Contains(f.tokenSeen, "inst-token-for-acme/monorepo") {
		t.Fatalf("authorization header was %q", f.tokenSeen)
	}
}

// A re-connect hits an existing variable: GitHub 409s the POST, so the code
// must fall through to PATCH rather than report failure.
func TestProvisionCIUpdatesExistingVariable(t *testing.T) {
	f, srv := newFakeGithub(t)
	defer srv.Close()
	f.variableExists = true
	s := serverFor(srv)

	res, err := s.provisionCI(context.Background(), "acme/monorepo",
		"https://runko.example.com/o/acme", "deploy-tok")
	if err != nil {
		t.Fatalf("provisionCI: %v", err)
	}
	if !f.patched {
		t.Fatal("expected the 409 to fall through to PATCH")
	}
	if res.Variable != "RUNKO_URL" {
		t.Fatalf("result: %+v", res)
	}
}

// No token means the secret is left alone - and SAID so. The bug this file
// exists to prevent was an unset secret that nothing complained about, so
// silence here would be the regression.
func TestProvisionCIReportsMissingToken(t *testing.T) {
	f, srv := newFakeGithub(t)
	defer srv.Close()
	s := serverFor(srv)

	res, err := s.provisionCI(context.Background(), "acme/monorepo",
		"https://runko.example.com/o/acme", "")
	if err != nil {
		t.Fatalf("provisionCI: %v", err)
	}
	if res.Secret != "" {
		t.Fatalf("secret reported written without a token: %+v", res)
	}
	if !strings.Contains(res.Skipped, "RUNKO_CI_TOKEN") {
		t.Fatalf("skipped reason does not name the secret: %q", res.Skipped)
	}
	if len(f.secrets) != 0 {
		t.Fatalf("wrote secrets anyway: %v", f.secrets)
	}
}

// A deployment without App credentials must not fail the connect - the
// mirror half is already armed and persisted by then.
func TestProvisionCISkipsWithoutAppWiring(t *testing.T) {
	s := &Server{}
	res, err := s.provisionCI(context.Background(), "acme/monorepo", "https://x/o/acme", "tok")
	if err != nil {
		t.Fatalf("provisionCI without wiring should not error: %v", err)
	}
	if res.Variable != "" || res.Secret != "" || res.Skipped == "" {
		t.Fatalf("result: %+v", res)
	}
}

func TestSealSecretRejectsBadKey(t *testing.T) {
	if _, err := sealSecret("not-base64!!", "x"); err == nil {
		t.Fatal("expected an error on undecodable key")
	}
	if _, err := sealSecret(base64.StdEncoding.EncodeToString([]byte("short")), "x"); err == nil {
		t.Fatal("expected an error on a wrong-length key")
	}
}
