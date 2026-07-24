package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/land"
)

func TestLandChangeSuccessDecodesOutcome(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/changes/Ichg1/land" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer sekret" {
			t.Fatalf("expected bearer token, got %q", r.Header.Get("Authorization"))
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"Landed": true, "LandedSHA": "abc123"})
	}))
	defer server.Close()

	outcome, err := LandChange(context.Background(), http.DefaultClient, server.URL, "sekret", "Ichg1", false)
	if err != nil {
		t.Fatalf("LandChange: %v", err)
	}
	if !outcome.Landed || outcome.LandedSHA != "abc123" {
		t.Fatalf("expected a decoded land.Outcome, got %+v", outcome)
	}
}

func TestLandChangeConflictDecodesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(clierr.Error{
			Code: "not_mergeable", Message: "change Ichg1 is not mergeable yet",
		})
	}))
	defer server.Close()

	_, err := LandChange(context.Background(), http.DefaultClient, server.URL, "sekret", "Ichg1", false)
	if err == nil {
		t.Fatalf("expected an error on 409")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Code != "not_mergeable" {
		t.Fatalf("expected code not_mergeable, got %+v", ce)
	}
}

func TestLandChangeNotFoundIsStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := LandChange(context.Background(), http.DefaultClient, server.URL, "sekret", "no-such-change", false)
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Code != "not_found" {
		t.Fatalf("expected code not_found, got %+v", ce)
	}
}

func TestCmdChangeLandRequiresFlags(t *testing.T) {
	err := execCLI("change", "land", "--change", "Ichg1")
	if err == nil {
		t.Fatalf("expected an error when --runkod-url/--token are missing")
	}
	var ue usageError
	if errors.As(err, &ue) {
		t.Fatalf("expected a validation error, not a usageError, got %v", err)
	}
}

func TestCmdChangeLandJSONOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{"Landed": true, "LandedSHA": "def456"})
	}))
	defer server.Close()

	var cmdErr error
	out := captureStdout(t, func() {
		cmdErr = execCLI("change", "land", "--runkod-url", server.URL, "--token", "sekret", "--change", "Ichg1", "--json")
	})
	if cmdErr != nil {
		t.Fatalf("cmdChangeLand: %v", cmdErr)
	}
	var outcome land.Outcome
	if err := json.Unmarshal([]byte(out), &outcome); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}
	if !outcome.Landed || outcome.LandedSHA != "def456" {
		t.Fatalf("expected the decoded outcome in JSON output, got %+v", outcome)
	}
}

// TestCmdChangeLandDefaultsToHEAD: the daily loop is create -> push -> land;
// land without --change resolves HEAD's Change-Id and notes it on stderr.
func TestCmdChangeLandDefaultsToHEAD(t *testing.T) {
	const headID = "I6a3f0123456789abcdef0123456789abcdef0123"
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("feature\n\nChange-Id: " + headID)

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		// --sync is on by default and the fixture is a real git checkout, so
		// land takes the recovery path only on requires_revalidation; a clean
		// land response is a single-shot 200 from the land endpoint.
		json.NewEncoder(w).Encode(map[string]interface{}{"Landed": true, "LandedSHA": "deadbeef"})
	}))
	defer server.Close()

	var warnings bytes.Buffer
	oldWarn := warnWriter
	warnWriter = &warnings
	defer func() { warnWriter = oldWarn }()

	var cmdErr error
	out := captureStdout(t, func() {
		// --sync=false keeps this a single land POST even though the fixture
		// is a real checkout (otherwise LandWithSync would try to fetch/rebase).
		cmdErr = execCLI("change", "land", "--runkod-url", server.URL, "--token", "sekret",
			"--repo", repo.Dir, "--sync=false", "--json")
	})
	if cmdErr != nil {
		t.Fatalf("cmdChangeLand: %v", cmdErr)
	}
	if gotPath != "/api/changes/"+headID+"/land" {
		t.Fatalf("expected land of HEAD's change %s, got path %q", headID, gotPath)
	}
	if !strings.Contains(warnings.String(), "landing HEAD's change "+headID) {
		t.Fatalf("expected stderr resolution note, got %q", warnings.String())
	}
	var outcome land.Outcome
	if err := json.Unmarshal([]byte(out), &outcome); err != nil {
		t.Fatalf("expected valid JSON output, got %q: %v", out, err)
	}
	if !outcome.Landed {
		t.Fatalf("expected landed outcome, got %+v", outcome)
	}
}

// TestCmdChangeLandNoChangeIDSuggests: outside a Change-Id checkout, omitting
// --change fails with the structured headChangeID error (message + suggestion).
func TestCmdChangeLandNoChangeIDSuggests(t *testing.T) {
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("no trailer yet")

	err := execCLI("change", "land", "--runkod-url", "http://example.invalid", "--token", "sekret",
		"--repo", repo.Dir, "--sync=false")
	if err == nil {
		t.Fatal("expected an error when HEAD has no Change-Id")
	}
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected *clierr.Error, got %T: %v", err, err)
	}
	if ce.Code != "no_change_id" {
		t.Fatalf("expected no_change_id, got %+v", ce)
	}
	if !strings.Contains(err.Error(), "  -> ") {
		t.Fatalf("expected a suggestion line, got %v", err)
	}
	var ue usageError
	if errors.As(err, &ue) {
		t.Fatalf("expected exit-1 validation error, not usageError: %v", err)
	}
}

// TestCmdChangeAutomergeDefaultsToHEAD: same HEAD default as land/requirements.
func TestCmdChangeAutomergeDefaultsToHEAD(t *testing.T) {
	const headID = "Iaa3f0123456789abcdef0123456789abcdef0123"
	repo := gitfixture.New(t)
	configureIdentity(t, repo.Dir)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("feature\n\nChange-Id: " + headID)

	for _, tc := range []struct {
		name    string
		disable bool
		note    string
		armed   bool
	}{
		{name: "arm", note: "arming automerge for HEAD's change ", armed: true},
		{name: "disable", disable: true, note: "disarming automerge for HEAD's change ", armed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				json.NewEncoder(w).Encode(map[string]interface{}{
					"ChangeKey": headID, "Automerge": tc.armed, "AutomergeBy": "alice",
				})
			}))
			defer server.Close()

			var warnings bytes.Buffer
			oldWarn := warnWriter
			warnWriter = &warnings
			defer func() { warnWriter = oldWarn }()

			args := []string{"change", "automerge", "--runkod-url", server.URL, "--token", "sekret",
				"--dir", repo.Dir, "--json"}
			if tc.disable {
				args = append(args, "--disable")
			}
			var cmdErr error
			out := captureStdout(t, func() {
				cmdErr = execCLI(args...)
			})
			if cmdErr != nil {
				t.Fatalf("cmdChangeAutomerge: %v", cmdErr)
			}
			if gotPath != "/api/changes/"+headID+"/automerge" {
				t.Fatalf("expected automerge of HEAD's change %s, got path %q", headID, gotPath)
			}
			if !strings.Contains(warnings.String(), tc.note+headID) {
				t.Fatalf("expected stderr resolution note %q, got %q", tc.note+headID, warnings.String())
			}
			if !strings.Contains(out, headID) {
				t.Fatalf("expected JSON naming the change, got %q", out)
			}
		})
	}
}

// TestCmdChangeAbandonStillRequiresChange: abandon is destructive and must
// stay explicit, but the bare required-flag error now carries a next step.
func TestCmdChangeAbandonStillRequiresChange(t *testing.T) {
	err := execCLI("change", "abandon")
	if err == nil {
		t.Fatal("expected --change required")
	}
	msg := err.Error()
	if !strings.Contains(msg, "change abandon: --change is required") {
		t.Fatalf("kept first line, got %q", msg)
	}
	if !strings.Contains(msg, "  -> ") || !strings.Contains(msg, "--change") {
		t.Fatalf("expected a suggestion naming --change, got %q", msg)
	}
	var ue usageError
	if errors.As(err, &ue) {
		t.Fatalf("expected exit-1 validation error, not usageError: %v", err)
	}
}

// TestRequiredFlagErrorsCarrySuggestions pins the house style for bare
// "X is required" validation failures: first line unchanged, suggestion on
// a "  -> " line, exit-1 (not usageError/exit-2).
func TestRequiredFlagErrorsCarrySuggestions(t *testing.T) {
	repo := gitfixture.New(t)
	cases := []struct {
		name string
		args []string
		want string // first-line fragment that must stay
	}{
		{"approve", []string{"change", "approve"}, "change approve: --change and --owner are required"},
		{"rerun-check", []string{"change", "rerun-check"}, "change rerun-check: --change and --name are required"},
		{"comment", []string{"change", "comment"}, "change comment: -m is required"},
		{"release create", []string{"release", "create"}, "release create: --project is required"},
		{"release list", []string{"release", "list"}, "release list: --project is required"},
		// name is checked before the repo is opened, but pass a real dir anyway
		{"project create", []string{"project", "create", "--repo", repo.Dir}, "project create: --name is required"},
		{"project delete", []string{"project", "delete"}, "project delete: --name is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := execCLI(c.args...)
			if err == nil {
				t.Fatal("expected a required-flag error")
			}
			msg := err.Error()
			if !strings.Contains(msg, c.want) {
				t.Fatalf("first line must stay %q, got %q", c.want, msg)
			}
			if !strings.Contains(msg, "\n  -> ") {
				t.Fatalf("expected a suggestion line, got %q", msg)
			}
			var ue usageError
			if errors.As(err, &ue) {
				t.Fatalf("expected exit-1 validation error, not usageError: %v", err)
			}
		})
	}
}
