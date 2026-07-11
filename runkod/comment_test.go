package runkod

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// commentWireView decodes review.go's commentWire for assertions.
type commentWireView struct {
	ID       string                    `json:"id"`
	Author   struct{ Type, ID string } `json:"author"`
	Body     string                    `json:"body"`
	Path     string                    `json:"path"`
	Side     string                    `json:"side"`
	Line     int                       `json:"line"`
	HeadSHA  string                    `json:"head_sha"`
	ParentID string                    `json:"parent_id"`
	Resolved bool                      `json:"resolved"`
}

func postReviewJSON(t *testing.T, srv *httptest.Server, path, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func decodeComment(t *testing.T, resp *http.Response, wantStatus int) commentWireView {
	t.Helper()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("expected %d, got %d: %s", wantStatus, resp.StatusCode, body)
	}
	var c commentWireView
	if err := json.Unmarshal(body, &c); err != nil {
		t.Fatalf("decode comment: %v: %s", err, body)
	}
	return c
}

func listComments(t *testing.T, srv *httptest.Server, changeID string) []commentWireView {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/changes/"+changeID+"/comments", nil)
	req.Header.Set("Authorization", "Bearer sekret")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET comments: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET comments: %d: %s", resp.StatusCode, body)
	}
	var page struct {
		Comments []commentWireView `json:"comments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode comments page: %v", err)
	}
	return page.Comments
}

func expectClierr(t *testing.T, resp *http.Response, wantStatus int, wantCode string) {
	t.Helper()
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		t.Fatalf("expected %d %s, got %d: %s", wantStatus, wantCode, resp.StatusCode, body)
	}
	var ce clierr.Error
	if err := json.Unmarshal(body, &ce); err != nil || ce.Code != wantCode {
		t.Fatalf("expected structured %s, got %s", wantCode, body)
	}
}

// TestCommentThreadRoundTrip is the §13.4.1 model over the wire: anchored
// root, reply via parent_id, the one-level rule, resolve on roots only,
// and the server-side head_sha stamp.
func TestCommentThreadRoundTrip(t *testing.T) {
	srv, _, changeID, _ := newApproveTestServer(t)
	defer srv.Close()

	root := decodeComment(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments", "sekret",
		`{"body":"nit: name this","author":"reviewer","path":"commerce/checkout/main.go","line":1}`), http.StatusCreated)
	if root.HeadSHA == "" || root.Side != "head" || root.Author.ID != "reviewer" || root.Author.Type != "user" {
		t.Fatalf("root comment missing server stamps (head_sha, default side, author): %+v", root)
	}

	reply := decodeComment(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments", "sekret",
		fmt.Sprintf(`{"body":"done","author":"author","parent_id":"%s"}`, root.ID)), http.StatusCreated)
	if reply.ParentID != root.ID {
		t.Fatalf("reply not threaded under root: %+v", reply)
	}

	// One level deep: replying to a reply names the root instead (§13.4.1).
	expectClierr(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments", "sekret",
		fmt.Sprintf(`{"body":"nested","author":"x","parent_id":"%s"}`, reply.ID)),
		http.StatusBadRequest, "thread_depth_exceeded")

	// A reply carrying its own anchor is refused - replies inherit the root's.
	expectClierr(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments", "sekret",
		fmt.Sprintf(`{"body":"x","author":"x","parent_id":"%s","path":"other.go"}`, root.ID)),
		http.StatusBadRequest, "invalid_anchor")

	// Resolve is root-only; the anonymous deploy token may resolve (v1 boundary).
	expectClierr(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments/"+reply.ID+"/resolve", "sekret", `{}`),
		http.StatusBadRequest, "not_a_thread_root")
	resolved := decodeComment(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments/"+root.ID+"/resolve", "sekret", `{}`), http.StatusOK)
	if !resolved.Resolved {
		t.Fatalf("expected the root resolved, got %+v", resolved)
	}

	all := listComments(t, srv, changeID)
	if len(all) != 2 || !all[0].Resolved || all[1].ParentID != root.ID {
		t.Fatalf("expected [resolved root, threaded reply], got %+v", all)
	}
}

// TestAmendOutdatesCommentsAndRederivesAttention pins §13.4.1's head_sha
// binding and §13.4.2's derivation together: a requested reviewer who
// commented at v1 leaves the attention set - and re-enters it the moment an
// amend moves the head, because their response no longer addresses the
// current version. The stored comment itself is untouched (marked outdated
// by comparison, never rewritten).
func TestAmendOutdatesCommentsAndRederivesAttention(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	const trailer = "Change-Id: I0123456789abcdef0123456789abcdef01234567"
	repo.WriteFile("commerce/checkout/main.go", "package main // v1\n")
	repo.Commit("add main.go\n\n" + trailer)
	_, head1 := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: head1, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}
	changeID := result.ChangeID

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	if resp := postReviewJSON(t, srv, "/api/changes/"+changeID+"/request-review", "sekret", `{"reviewer":"reviewer"}`); resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("request-review: %d: %s", resp.StatusCode, body)
	}
	reqs := getMergeRequirements(t, srv, changeID)
	if !contains(reqs.AttentionSet, "reviewer") {
		t.Fatalf("a requested reviewer who hasn't responded belongs in the attention set, got %v", reqs.AttentionSet)
	}

	// The reviewer responds at v1 - their turn is over.
	v1Comment := decodeComment(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments", "sekret",
		`{"body":"looks wrong","author":"reviewer","path":"commerce/checkout/main.go","line":1}`), http.StatusCreated)
	reqs = getMergeRequirements(t, srv, changeID)
	if contains(reqs.AttentionSet, "reviewer") {
		t.Fatalf("a reviewer who commented at the current head must leave the attention set, got %v", reqs.AttentionSet)
	}

	// Amend under the same Change-Id: the comment goes stale, attention
	// returns to the reviewer.
	repo.WriteFile("commerce/checkout/main.go", "package main // v2\n")
	repo.Commit("amend\n\n" + trailer)
	_, head2 := pushCommit(t, repo, bare, "refs/for/main")
	result2 := processor.Process(context.Background(), RefUpdate{OldSHA: head1, NewSHA: head2, Ref: "refs/for/main"}, nil)
	if !result2.Accepted || result2.ChangeID != changeID {
		t.Fatalf("amend push not accepted as the same Change: %+v", result2)
	}

	all := listComments(t, srv, changeID)
	if len(all) != 1 || all[0].HeadSHA != v1Comment.HeadSHA || all[0].HeadSHA == head2 {
		t.Fatalf("the stored comment must keep its v1 head_sha binding, got %+v (head2 %s)", all, head2)
	}
	reqs = getMergeRequirements(t, srv, changeID)
	if !contains(reqs.AttentionSet, "reviewer") {
		t.Fatalf("an amend must put the requested reviewer back in the attention set, got %v", reqs.AttentionSet)
	}
}

// TestRequireResolvedThreadsBlocker pins the org knob (§13.4.1): OFF by
// default - an unresolved thread doesn't block; ON - it blocks with a named
// blocker and clears on resolve.
func TestRequireResolvedThreadsBlocker(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	mem := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: mem}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}
	changeID := result.ChangeID

	// SettingsOrg + Directory wired from the start; the org's settings row
	// is empty, so the knob genuinely defaults off through the same read
	// path production uses.
	server := &Server{
		RepoDir: bare, TrunkRef: "main", Store: mem, Processor: processor, Token: "sekret",
		SettingsOrg: "test-org", Directory: mem,
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	root := decodeComment(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments", "sekret",
		`{"body":"blocker?","author":"reviewer"}`), http.StatusCreated)

	// Default off: the unresolved thread is not a blocker.
	reqs := getMergeRequirements(t, srv, changeID)
	for _, b := range reqs.Blockers {
		if strings.Contains(b, "unresolved review thread") {
			t.Fatalf("knob is off by default - no thread blocker expected, got %v", reqs.Blockers)
		}
	}

	if err := mem.UpdateOrgSettings(context.Background(), "test-org", OrgSettings{RequireResolvedThreads: true}); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}
	reqs = getMergeRequirements(t, srv, changeID)
	found := false
	for _, b := range reqs.Blockers {
		if strings.Contains(b, "1 unresolved review thread") {
			found = true
		}
	}
	if !found || reqs.Mergeable {
		t.Fatalf("expected the unresolved-thread blocker with the knob on, got mergeable=%v blockers=%v", reqs.Mergeable, reqs.Blockers)
	}

	if resp := postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments/"+root.ID+"/resolve", "sekret", `{}`); resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("resolve: %d: %s", resp.StatusCode, body)
	}
	reqs = getMergeRequirements(t, srv, changeID)
	for _, b := range reqs.Blockers {
		if strings.Contains(b, "unresolved review thread") {
			t.Fatalf("resolving the thread must clear the blocker, got %v", reqs.Blockers)
		}
	}
}

// TestAgentCommentsAllowedApprovalStillRefused is §13.4.1's agent rule at
// the wire: the same agent principal whose approval is structurally refused
// (§8.7) comments freely, and the comment carries the agent badge.
func TestAgentCommentsAllowedApprovalStillRefused(t *testing.T) {
	srv, _, changeID, _ := newAgentPrincipalServer(t)
	defer srv.Close()

	expectClierr(t, postApprove(t, srv, changeID, "bottok", `{"owner_ref":"group:commerce-eng"}`),
		http.StatusForbidden, "agent_approval_denied")

	c := decodeComment(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments", "bottok",
		`{"body":"consider wrapping this error"}`), http.StatusCreated)
	if c.Author.Type != "agent" || c.Author.ID != "bot" {
		t.Fatalf("expected the agent badge on the comment author, got %+v", c.Author)
	}
}

// TestResolveDeniedForBystander: a named principal who is neither the
// thread author, the change author, nor an owner of the anchored path may
// not resolve the thread (§13.4.1).
func TestResolveDeniedForBystander(t *testing.T) {
	srv, _, changeID, _ := newAgentPrincipalServer(t)
	defer srv.Close()

	root := decodeComment(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments", "sekret",
		`{"body":"root","author":"reviewer","path":"commerce/checkout/main.go","line":1}`), http.StatusCreated)

	expectClierr(t, postReviewJSON(t, srv, "/api/changes/"+changeID+"/comments/"+root.ID+"/resolve", "randotok", `{}`),
		http.StatusForbidden, "resolve_denied")
}

// newAgentPrincipalServer is newApproveTestServer plus two named principals:
// an agent ("bot") and an unprivileged human bystander ("rando").
func newAgentPrincipalServer(t *testing.T) (srv *httptest.Server, bare string, changeID string, store Store) {
	t.Helper()
	bare = newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("commerce/checkout/main.go", "package main\n")
	repo.Commit("add main.go\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store = NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server := &Server{
		RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret",
		Principals: []Principal{
			{Name: "bot", Token: "bottok", IsAgent: true},
			{Name: "rando", Token: "randotok"},
		},
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return httptest.NewServer(handler), bare, result.ChangeID, store
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
