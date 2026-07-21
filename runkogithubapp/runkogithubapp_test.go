package runkogithubapp

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testRSAKey, _ = rsa.GenerateKey(rand.Reader, 2048)

func testKeyPEM(t *testing.T) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(testRSAKey)})
}

// verifyJWT asserts the Authorization header carries a valid RS256 App
// JWT for appID - signature checked against the test public key, claims
// checked for issuer and liveness. This runs on EVERY stub request, so
// a malformed JWT fails whichever test sent it.
func verifyJWT(t *testing.T, r *http.Request, appID string) {
	t.Helper()
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("app endpoints need a JWT bearer, got %q", raw)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("jwt signature encoding: %v", err)
	}
	if err := rsa.VerifyPKCS1v15(&testRSAKey.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("jwt signature does not verify against the app key: %v", err)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("jwt claims encoding: %v", err)
	}
	var claims struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatalf("jwt claims: %v", err)
	}
	// Clock-independent (tests time-travel the app's now): issuer, the
	// 60s backdate, and the 9-minute lifetime GitHub caps at 10.
	if claims.Iss != appID || claims.Exp-claims.Iat != int64((time.Minute+9*time.Minute)/time.Second) {
		t.Fatalf("jwt claims out of shape: %+v", claims)
	}
}

// ghStub is a fake GitHub App API: installation lookup + token minting,
// with counters and swappable behavior for the failure tests.
type ghStub struct {
	t         *testing.T
	appID     string
	installID atomic.Int64
	lookups   atomic.Int32
	mints     atomic.Int32
	// staleMints holds installation ids that 404 on mint (App
	// reinstalled since the id was cached).
	staleMints map[int64]bool
	notFound   bool // lookup answers 404 (App not installed)
	expiresIn  time.Duration
	mintBody   atomic.Value // string: the most recent mint request body
	srv        *httptest.Server
}

func newGHStub(t *testing.T, appID string) *ghStub {
	s := &ghStub{t: t, appID: appID, staleMints: map[int64]bool{}, expiresIn: time.Hour}
	s.installID.Store(7)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/acme/runko/installation", func(w http.ResponseWriter, r *http.Request) {
		verifyJWT(t, r, appID)
		s.lookups.Add(1)
		if s.notFound {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": s.installID.Load()})
	})
	mux.HandleFunc("POST /app/installations/{id}/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		verifyJWT(t, r, appID)
		s.mints.Add(1)
		body, _ := io.ReadAll(r.Body)
		s.mintBody.Store(string(body))
		var id int64
		json.Unmarshal([]byte(r.PathValue("id")), &id)
		if s.staleMints[id] {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_test",
			"expires_at": time.Now().Add(s.expiresIn).Format(time.RFC3339),
		})
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func newTestApp(t *testing.T, stub *ghStub) *App {
	t.Helper()
	app, err := New("12345", testKeyPEM(t), stub.srv.URL)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

func TestTokenMintsAndCaches(t *testing.T) {
	stub := newGHStub(t, "12345")
	app := newTestApp(t, stub)
	for i := 0; i < 3; i++ {
		tok, err := app.Token(t.Context(), "acme/runko")
		if err != nil {
			t.Fatalf("Token call %d: %v", i, err)
		}
		if tok != "ghs_test" {
			t.Fatalf("token: got %q", tok)
		}
	}
	if stub.lookups.Load() != 1 || stub.mints.Load() != 1 {
		t.Fatalf("a fresh hour-long token must be cached: %d lookups, %d mints", stub.lookups.Load(), stub.mints.Load())
	}
}

// TestMintScopesTokenToRepo pins the security property multi-repo
// installations rely on (one user account = one installation covering
// every selected repo): the mint body narrows the token to exactly the
// repo it was asked for, so a token minted for one repo can never write
// a sibling repo on the same installation.
func TestMintScopesTokenToRepo(t *testing.T) {
	stub := newGHStub(t, "12345")
	app := newTestApp(t, stub)
	if _, err := app.Token(t.Context(), "acme/runko"); err != nil {
		t.Fatalf("Token: %v", err)
	}
	var got struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(stub.mintBody.Load().(string)), &got); err != nil {
		t.Fatalf("mint request body is not JSON: %v", err)
	}
	if len(got.Repositories) != 1 || got.Repositories[0] != "runko" {
		t.Fatalf(`mint body must scope repositories to ["runko"], got %v`, got.Repositories)
	}
}

func TestTokenRefreshesNearExpiry(t *testing.T) {
	stub := newGHStub(t, "12345")
	app := newTestApp(t, stub)
	if _, err := app.Token(t.Context(), "acme/runko"); err != nil {
		t.Fatalf("Token: %v", err)
	}
	// Inside the refresh margin of the one-hour expiry: must re-mint,
	// and the installation id must come from cache (no second lookup).
	app.now = func() time.Time { return time.Now().Add(time.Hour - refreshMargin + time.Second) }
	if _, err := app.Token(t.Context(), "acme/runko"); err != nil {
		t.Fatalf("Token near expiry: %v", err)
	}
	if stub.mints.Load() != 2 || stub.lookups.Load() != 1 {
		t.Fatalf("want re-mint without re-lookup: %d mints, %d lookups", stub.mints.Load(), stub.lookups.Load())
	}
}

func TestTokenSurfacesNotInstalled(t *testing.T) {
	stub := newGHStub(t, "12345")
	stub.notFound = true
	app := newTestApp(t, stub)
	_, err := app.Token(t.Context(), "acme/runko")
	if err == nil || !strings.Contains(err.Error(), "not installed on acme/runko") {
		t.Fatalf("want an install-the-App suggestion, got %v", err)
	}
}

func TestTokenRecoversFromReinstall(t *testing.T) {
	stub := newGHStub(t, "12345")
	app := newTestApp(t, stub)
	if _, err := app.Token(t.Context(), "acme/runko"); err != nil {
		t.Fatalf("Token: %v", err)
	}
	// The App is reinstalled: the cached installation id 7 now 404s on
	// mint, the fresh lookup answers 8. Force a refresh past expiry.
	stub.staleMints[7] = true
	stub.installID.Store(8)
	app.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	tok, err := app.Token(t.Context(), "acme/runko")
	if err != nil || tok != "ghs_test" {
		t.Fatalf("re-resolve after reinstall: token %q, err %v", tok, err)
	}
	if stub.lookups.Load() != 2 {
		t.Fatalf("want exactly one re-lookup, got %d", stub.lookups.Load())
	}
}

func TestNewParsesPKCS8(t *testing.T) {
	der, err := x509.MarshalPKCS8PrivateKey(testRSAKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8: %v", err)
	}
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if _, err := New("12345", pkcs8, "https://api.github.com"); err != nil {
		t.Fatalf("New with PKCS#8 key: %v", err)
	}
}

func TestNewRejectsGarbageKey(t *testing.T) {
	if _, err := New("12345", []byte("not a key"), "https://api.github.com"); err == nil {
		t.Fatal("want error for non-PEM key")
	}
	if _, err := New("", testKeyPEM(t), "https://api.github.com"); err == nil {
		t.Fatal("want error for empty app id")
	}
}

func TestRepoPath(t *testing.T) {
	public, err := New("1", testKeyPEM(t), "https://api.github.com")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ghes, err := New("1", testKeyPEM(t), "https://ghe.example.com/api/v3")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []struct {
		app  *App
		url  string
		want string
	}{
		{public, "https://github.com/acme/runko.git", "acme/runko"},
		{public, "https://github.com/acme/runko", "acme/runko"},
		{public, "https://gitlab.com/acme/runko.git", ""}, // wrong host
		{public, "git@github.com:acme/runko.git", ""},     // not https
		{public, "https://github.com/acme", ""},           // no repo
		{public, "https://github.com/a/b/c", ""},          // too deep
		{ghes, "https://ghe.example.com/acme/runko.git", "acme/runko"},
		{ghes, "https://github.com/acme/runko.git", ""}, // GHES app, public URL
	}
	for _, tc := range cases {
		if got := tc.app.RepoPath(tc.url); got != tc.want {
			t.Errorf("RepoPath(%q): got %q, want %q", tc.url, got, tc.want)
		}
	}
}
