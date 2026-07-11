package runkod

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// newReleaseTestServer seeds two projects - checkout-api WITH the release
// capability (and an explicit tag_prefix), money-lib WITHOUT - plus one
// open Change touching checkout-api, ready to approve+land.
func newReleaseTestServer(t *testing.T) (srv *httptest.Server, server *Server, repo *gitfixture.Repo, bare string, changeID string, mem *MemStore) {
	t.Helper()
	bare = newBareRepo(t)
	repo = gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\ncapabilities:\n  - release\ncapability_config:\n  release:\n    tag_prefix: checkout/v\n")
	repo.WriteFile("libs/money/PROJECT.yaml",
		"schema: project/v1\nname: money-lib\ntype: library\nowners:\n  - group:billing-eng\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("checkout: add SKU validation\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	mem = NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: mem, OrgName: "test-org"}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server = &Server{
		RepoDir: bare, TrunkRef: "main", Store: mem, Processor: processor, Token: "sekret",
		SettingsOrg: "test-org", Directory: mem,
		Principals: []Principal{{Name: "rando", Token: "randotok"}},
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return httptest.NewServer(handler), server, repo, bare, result.ChangeID, mem
}

func approveAndLand(t *testing.T, srv *httptest.Server, changeID string) {
	t.Helper()
	if resp := postApprove(t, srv, changeID, "sekret", `{"owner_ref":"group:commerce-eng","approved_by":"alice"}`); resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("approve: %d: %s", resp.StatusCode, body)
	}
	if resp := postLand(t, srv, changeID, "sekret"); resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("land: %d: %s", resp.StatusCode, body)
	}
}

// gitInBare runs one git command against the bare repo and returns
// trimmed stdout.
func gitInBare(t *testing.T, bare string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = bare
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func postRelease(t *testing.T, srv *httptest.Server, project, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/projects/"+project+"/releases", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST releases: %v", err)
	}
	return resp
}

func decodeRelease(t *testing.T, resp *http.Response, wantStatus int) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("expected %d, got %d: %s", wantStatus, resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	return out
}

// TestReleaseEndToEnd is stage 17b's bar: land a Change, cut 0.1.0 (the
// changelog names the landed Change; the annotated tag really exists in
// the bare repo; release.created rides the outbox), land another, cut
// again - 0.1.1's changelog covers only the delta.
func TestReleaseEndToEnd(t *testing.T) {
	srv, _, repo, bare, changeID, mem := newReleaseTestServer(t)
	defer srv.Close()
	approveAndLand(t, srv, changeID)

	first := decodeRelease(t, postRelease(t, srv, "checkout-api", "sekret", `{}`), http.StatusCreated)
	if first["version"] != "0.1.0" || first["tag_ref"] != "refs/tags/checkout/v0.1.0" {
		t.Fatalf("unexpected first release: %+v", first)
	}
	changelog, _ := first["changelog"].(string)
	if !strings.Contains(changelog, "checkout: add SKU validation") || !strings.Contains(changelog, changeID) {
		t.Fatalf("changelog must name the landed Change, got %q", changelog)
	}
	if first["head_change_key"] != changeID {
		t.Fatalf("expected head_change_key %s, got %v", changeID, first["head_change_key"])
	}
	// The annotated tag object really exists at the released commit.
	if tagType := gitInBare(t, bare, "cat-file", "-t", "refs/tags/checkout/v0.1.0"); tagType != "tag" {
		t.Fatalf("expected an annotated tag object, got %q", tagType)
	}
	if peeled := gitInBare(t, bare, "rev-parse", "refs/tags/checkout/v0.1.0^{commit}"); peeled != first["target_sha"] {
		t.Fatalf("tag peels to %s, release records target %v", peeled, first["target_sha"])
	}
	// release.created rides the generic outbox.
	deliveries, err := mem.ListDueWebhookDeliveries(context.Background(), time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries: %v", err)
	}
	found := false
	for _, d := range deliveries {
		if d.EventType == "release.created" && strings.Contains(string(d.Payload), `"0.1.0"`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a release.created delivery in the outbox")
	}

	// Second landed Change, second release: patch bump, delta-only changelog.
	repo.WriteFile("commerce/checkout/errors.go", "package main\n")
	repo.Commit("checkout: structured errors\n\nChange-Id: Iaaaa456789abcdef0123456789abcdef01234567")
	_, head2 := pushCommit(t, repo, bare, "refs/for/main")
	proc := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: mem, OrgName: "test-org"}
	res2 := proc.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: head2, Ref: "refs/for/main"}, nil)
	if !res2.Accepted {
		t.Fatalf("second push rejected: %+v", res2)
	}
	approveAndLand(t, srv, res2.ChangeID)

	second := decodeRelease(t, postRelease(t, srv, "checkout-api", "sekret", `{}`), http.StatusCreated)
	if second["version"] != "0.1.1" {
		t.Fatalf("expected the patch bump 0.1.1, got %v", second["version"])
	}
	log2, _ := second["changelog"].(string)
	if !strings.Contains(log2, "structured errors") || strings.Contains(log2, "SKU validation") {
		t.Fatalf("second changelog must cover only the delta, got %q", log2)
	}

	// The list endpoint returns newest first.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/projects/checkout-api/releases", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET releases: %v", err)
	}
	defer resp.Body.Close()
	var page struct {
		Releases []map[string]any `json:"releases"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(page.Releases) != 2 || page.Releases[0]["version"] != "0.1.1" {
		t.Fatalf("expected [0.1.1, 0.1.0], got %+v", page.Releases)
	}
}

// TestReleaseValidation pins the structured failures: no release
// capability, bad explicit semver, duplicate version, and unknown project.
func TestReleaseValidation(t *testing.T) {
	srv, _, _, _, changeID, _ := newReleaseTestServer(t)
	defer srv.Close()
	approveAndLand(t, srv, changeID)

	expectClierr(t, postRelease(t, srv, "money-lib", "sekret", `{}`), http.StatusConflict, "release_not_enabled")
	expectClierr(t, postRelease(t, srv, "checkout-api", "sekret", `{"version":"one.two"}`), http.StatusBadRequest, "invalid_version")

	if resp := postRelease(t, srv, "checkout-api", "sekret", `{"version":"2.0.0"}`); resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("explicit version: %d: %s", resp.StatusCode, body)
	}
	// Same version again: the tag (and row) already exist.
	resp := postRelease(t, srv, "checkout-api", "sekret", `{"version":"2.0.0"}`)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusConflict || !(strings.Contains(string(body), "tag_exists") || strings.Contains(string(body), "version_exists")) {
		t.Fatalf("expected tag_exists/version_exists conflict, got %d: %s", resp.StatusCode, body)
	}

	if resp := postRelease(t, srv, "no-such-project", "sekret", `{}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown project, got %d", resp.StatusCode)
	}
}

// TestReleaseDeniedUnderTagPolicy: cutting a release answers to the SAME
// tag policy raw pushes do (§14.10.3) - under enforcement a plain named
// principal gets release_denied while the operator credential still cuts.
func TestReleaseDeniedUnderTagPolicy(t *testing.T) {
	srv, _, _, _, changeID, mem := newReleaseTestServer(t)
	defer srv.Close()
	approveAndLand(t, srv, changeID)

	if err := mem.UpdateOrgSettings(context.Background(), "test-org", OrgSettings{EnforceTagPolicy: true}); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}
	expectClierr(t, postRelease(t, srv, "checkout-api", "randotok", `{}`), http.StatusForbidden, "release_denied")

	if resp := postRelease(t, srv, "checkout-api", "sekret", `{}`); resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("operator release under enforcement: %d: %s", resp.StatusCode, body)
	}
}
