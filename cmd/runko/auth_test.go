package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/checks"
	"github.com/saxocellphone/runko/internal/clierr"
)

func TestAuthHeaderValueAndGitUserPass(t *testing.T) {
	if got := authHeaderValue("sekret"); got != "Bearer sekret" {
		t.Fatalf("raw token: %q", got)
	}
	basic := Credential{Name: "val", Secret: "pw"}.AuthHeader()
	if got := authHeaderValue(basic); got != basic {
		t.Fatalf("pre-rendered header must pass through, got %q", got)
	}
	if u, p := gitUserPass(basic); u != "val" || p != "pw" {
		t.Fatalf("gitUserPass(basic) = %q %q", u, p)
	}
	if u, p := gitUserPass("sekret"); u != "runko" || p != "sekret" {
		t.Fatalf("gitUserPass(token) = %q %q", u, p)
	}
}

// whoamiStub answers /api/whoami accepting exactly one credential.
func whoamiStub(t *testing.T, wantAuth, name string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/whoami" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"name": name, "anonymous": name == ""})
	}))
}

func TestAuthLoginStoresValidatedCredential(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // os.UserConfigDir sandbox
	want := Credential{Name: "val", Secret: "hunter2hunter2"}.AuthHeader()
	srv := whoamiStub(t, want, "val")
	defer srv.Close()

	// A wrong password is rejected and stores NOTHING.
	_, err := AuthLogin(context.Background(), srv.Client(), srv.URL, "val", "wrong",
		bufio.NewReader(strings.NewReader("")), os.Stdout)
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "bad_credential" {
		t.Fatalf("wrong password: want bad_credential, got %v", err)
	}
	if _, found, _ := loadCredential(); found {
		t.Fatalf("a rejected login must not store a credential")
	}

	// The secret can come from the stdin prompt.
	if _, err := AuthLogin(context.Background(), srv.Client(), srv.URL, "val", "",
		bufio.NewReader(strings.NewReader("hunter2hunter2\n")), os.Stdout); err != nil {
		t.Fatalf("AuthLogin: %v", err)
	}
	cred, found, err := loadCredential()
	if err != nil || !found || cred.Name != "val" || cred.Secret != "hunter2hunter2" || cred.URL != srv.URL {
		t.Fatalf("stored credential: %+v found=%v err=%v", cred, found, err)
	}
	path, _ := credentialPath()
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file must be 0600, got %v (%v)", info.Mode(), err)
	}

	// resolveCredential: stored login is the fallback; explicit flags win.
	got, err := resolveCredential("", "")
	if err != nil || got.AuthHeader() != want {
		t.Fatalf("resolve from store: %+v err=%v", got, err)
	}
	flagged, err := resolveCredential("http://other", "raw-token")
	if err != nil || flagged.URL != "http://other" || flagged.AuthHeader() != "Bearer raw-token" {
		t.Fatalf("flags must win: %+v err=%v", flagged, err)
	}
}

func TestResolveCredentialWithoutLoginIsStructured(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := resolveCredential("", "")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "not_logged_in" {
		t.Fatalf("want not_logged_in, got %v", err)
	}
}

// change create commits WIP as one Change with a stable Change-Id, and
// change requirements defaults to HEAD's trailer.
func TestChangeCreateAndHeadChangeID(t *testing.T) {
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")

	// Empty tree: structured refusal.
	_, err := CreateChange(dir, "first change")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "nothing_to_commit" {
		t.Fatalf("want nothing_to_commit, got %v", err)
	}

	writeFile(t, dir, "svc/main.go", "package main\n")
	id, err := CreateChange(dir, "first change")
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if !strings.HasPrefix(id, "I") || len(id) != 41 {
		t.Fatalf("unexpected change id %q", id)
	}
	msg := mustGit(t, dir, "log", "-1", "--format=%B")
	if !strings.Contains(msg, "Change-Id: "+id) {
		t.Fatalf("commit message must carry the Change-Id trailer, got %q", msg)
	}
	headID, err := headChangeID(dir)
	if err != nil || headID != id {
		t.Fatalf("headChangeID: %q err=%v", headID, err)
	}

	// A second create stacks: a new commit, a NEW Change identity.
	writeFile(t, dir, "svc/handler.go", "package main // handler\n")
	id2, err := CreateChange(dir, "second change")
	if err != nil {
		t.Fatalf("second CreateChange: %v", err)
	}
	if id2 == id {
		t.Fatalf("stacked change must get its own Change-Id")
	}
}

func TestChangeRequirementsFetchAndPrintShape(t *testing.T) {
	reqs := checks.MergeRequirements{
		ChangeID:          "I123",
		RequiredOwners:    []string{"group:commerce"},
		OutstandingOwners: []string{"group:commerce"},
		RequiredChecks:    []string{"unit"},
		PendingChecks:     []string{"unit"},
		Blockers:          []string{"owner approval outstanding: group:commerce"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/changes/I123/merge-requirements" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sekret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(reqs)
	}))
	defer srv.Close()

	got, err := ChangeRequirements(context.Background(), srv.Client(),
		Credential{URL: srv.URL, Secret: "sekret"}, "I123")
	if err != nil {
		t.Fatalf("ChangeRequirements: %v", err)
	}
	if got.Mergeable || len(got.RequiredOwners) != 1 || len(got.PendingChecks) != 1 {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	_, err = ChangeRequirements(context.Background(), srv.Client(),
		Credential{URL: srv.URL, Secret: "sekret"}, "I999")
	var ce *clierr.Error
	if !errors.As(err, &ce) || ce.Code != "unknown_change" {
		t.Fatalf("want unknown_change for a 404, got %v", err)
	}
}
