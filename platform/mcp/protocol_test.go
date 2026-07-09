package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/saxocellphone/runko/platform/receive"
	"github.com/saxocellphone/runko/platform/search"
	"github.com/saxocellphone/runko/runkod"
)

const zeroOID = "0000000000000000000000000000000000000000"

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// fixedSearcher is a canned search.CodeSearcher so search_code round-trips
// through the daemon's real /api/search handler (including its project
// tagging) without a Zoekt install.
type fixedSearcher struct{ hits []search.Hit }

func (f fixedSearcher) Search(_ context.Context, q string, _ search.SearchOptions) (*search.Result, error) {
	return &search.Result{Query: q, Hits: append([]search.Hit(nil), f.hits...)}, nil
}

// startDaemon seeds a real bare repo (two projects on trunk - checkout-api
// with owners+ci.checks+a declared dep, money-lib with neither - §7.3/§14.9
// coverage in one fixture), pushes one Change through a real
// runkod.Processor, and serves the REST API over httptest.
func startDaemon(t *testing.T) (url string, changeID string) {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "monorepo.git")
	if err := runkod.EnsureBareRepo(bare, "main"); err != nil {
		t.Fatalf("EnsureBareRepo: %v", err)
	}

	seed := t.TempDir()
	mustGit(t, seed, "init", "-q", "-b", "main")
	mustGit(t, seed, "config", "user.email", "t@example.com")
	mustGit(t, seed, "config", "user.name", "t")
	writeFile(t, seed, "commerce/checkout/PROJECT.yaml",
		"schema: project/v1\nname: checkout-api\ntype: service\nowners:\n  - group:commerce-eng\ndependencies:\n  - money-lib\nci:\n  checks:\n    - name: unit\n      command: go test ./...\n")
	writeFile(t, seed, "libs/money/PROJECT.yaml", "schema: project/v1\nname: money-lib\ntype: library\n")
	writeFile(t, seed, "libs/money/money.go", "package money\n")
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "-q", "-m", "initial")
	mustGit(t, seed, "push", "-q", bare, "HEAD:refs/heads/main")

	writeFile(t, seed, "commerce/checkout/main.go", "package main\n")
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "-q", "-m", "add main.go\n\nChange-Id: Iaaaabbbbccccddddeeeeffff0000111122223333")
	headSHA := mustGit(t, seed, "rev-parse", "HEAD")
	mustGit(t, seed, "push", "-q", bare, "+HEAD:refs/for/main")

	store := runkod.NewMemStore()
	processor := &runkod.Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), runkod.RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server := &runkod.Server{
		RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret",
		Searcher: fixedSearcher{hits: []search.Hit{
			{Path: "commerce/checkout/main.go", LineNumber: 1, Line: "package main"},
			{Path: "libs/money/money.go", LineNumber: 1, Line: "package money"},
		}},
	}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL, result.ChangeID
}

// rpcSession drives a Server over in-memory pipes, the same
// newline-delimited framing a real MCP client would use over stdio.
type rpcSession struct {
	t   *testing.T
	in  *io.PipeWriter
	out *bufio.Scanner
}

func startSession(t *testing.T, daemonURL string) *rpcSession {
	t.Helper()
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	srv := &Server{Client: &Client{BaseURL: daemonURL, Token: "sekret"}}
	go func() {
		if err := srv.Serve(context.Background(), serverIn, serverOut); err != nil {
			t.Errorf("Serve: %v", err)
		}
		serverOut.Close()
	}()
	t.Cleanup(func() { clientOut.Close() })
	sc := bufio.NewScanner(clientIn)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	return &rpcSession{t: t, in: clientOut, out: sc}
}

func (s *rpcSession) send(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		s.t.Fatalf("write request: %v", err)
	}
}

type rpcReply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func (s *rpcSession) recv() rpcReply {
	s.t.Helper()
	if !s.out.Scan() {
		s.t.Fatalf("expected a response line, got EOF (%v)", s.out.Err())
	}
	var reply rpcReply
	if err := json.Unmarshal(s.out.Bytes(), &reply); err != nil {
		s.t.Fatalf("unmarshal response %q: %v", s.out.Text(), err)
	}
	return reply
}

// call round-trips one tools/call and returns the decoded content text
// plus the isError flag.
func (s *rpcSession) call(id int, tool string, args string) (json.RawMessage, bool) {
	s.t.Helper()
	s.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"%s","arguments":%s}}`, id, tool, args))
	reply := s.recv()
	if reply.Error != nil {
		s.t.Fatalf("tools/call %s: rpc error %+v", tool, reply.Error)
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(reply.Result, &result); err != nil {
		s.t.Fatalf("unmarshal tools/call result: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Type != "text" {
		s.t.Fatalf("expected exactly one text content block, got %+v", result.Content)
	}
	return json.RawMessage(result.Content[0].Text), result.IsError
}

// newSchemaCompiler returns a compiler that can resolve the catalog's
// relative `$ref: common.schema.json#/...` entries: catalog.json declares
// `$id: https://runko.dev/schema/mcp/catalog.json`, so relative refs
// resolve to runko.dev URLs - map the sibling file in under that URL
// rather than fetching anything over the network.
func newSchemaCompiler(t *testing.T) *jsonschema.Compiler {
	t.Helper()
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	common, err := os.Open(filepath.Join("..", "..", "docs", "spec", "mcp-tools", "common.schema.json"))
	if err != nil {
		t.Fatalf("open common.schema.json: %v", err)
	}
	defer common.Close()
	if err := c.AddResource("https://runko.dev/schema/mcp/common.schema.json", common); err != nil {
		t.Fatalf("add common.schema.json resource: %v", err)
	}
	return c
}

// compileOutputSchema compiles a v1 tool's output_schema straight out of
// catalog.json (a JSON-pointer fragment into the catalog document), so
// `$ref: common.schema.json#/...` entries resolve against the real sibling
// file - the same contract-testing bar stage 8 set for webhooks.
func compileOutputSchema(t *testing.T, toolName string) *jsonschema.Schema {
	t.Helper()
	idx := -1
	for i, ct := range loadCatalog(t) {
		if ct.Name == toolName {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("tool %s not in catalog", toolName)
	}
	abs, err := filepath.Abs(filepath.Join("..", "..", "docs", "spec", "mcp-tools", "catalog.json"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	sch, err := newSchemaCompiler(t).Compile(fmt.Sprintf("%s#/tools/%d/output_schema", abs, idx))
	if err != nil {
		t.Fatalf("compile %s output_schema: %v", toolName, err)
	}
	return sch
}

func validatePayload(t *testing.T, toolName string, payload json.RawMessage) {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal(payload, &v); err != nil {
		t.Fatalf("%s: unmarshal payload: %v", toolName, err)
	}
	if err := compileOutputSchema(t, toolName).Validate(v); err != nil {
		t.Fatalf("%s: output violates catalog output_schema:\n%v\npayload: %s", toolName, err, payload)
	}
}

// TestMCPEndToEndAllSixTools is the stage-12 bar (§28.3): a real MCP
// session (initialize handshake, tools/list, tools/call) over the wire
// framing a real client would use, against a real runkod REST API backed
// by real git - and every tool's output validated against the catalog's
// own output_schema, so the adapter can't drift from docs/spec/ without
// this failing.
func TestMCPEndToEndAllSixTools(t *testing.T) {
	daemonURL, changeID := startDaemon(t)
	s := startSession(t, daemonURL)

	// -- Handshake.
	s.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`)
	init := s.recv()
	if init.Error != nil || !strings.Contains(string(init.Result), protocolVersion) {
		t.Fatalf("initialize failed: %+v", init)
	}
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`) // notification: must get NO response

	// -- tools/list serves exactly the six v1 tools.
	s.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	listReply := s.recv()
	if string(listReply.ID) != "2" {
		t.Fatalf("expected the reply to id 2 (the notification must be unanswered), got id %s", listReply.ID)
	}
	var toolList struct {
		Tools []struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listReply.Result, &toolList); err != nil {
		t.Fatalf("unmarshal tools/list: %v", err)
	}
	if len(toolList.Tools) != 6 {
		t.Fatalf("expected exactly 6 v1 tools, got %d", len(toolList.Tools))
	}

	// -- list_projects.
	payload, isErr := s.call(3, "list_projects", `{}`)
	if isErr {
		t.Fatalf("list_projects errored: %s", payload)
	}
	validatePayload(t, "list_projects", payload)
	var lp ListProjectsOutput
	if err := json.Unmarshal(payload, &lp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(lp.Projects) != 2 || lp.Projects[0].Name != "checkout-api" || lp.Projects[1].Name != "money-lib" {
		t.Fatalf("expected both seeded projects sorted by name, got %+v", lp.Projects)
	}

	// -- get_project (full detail carries owners, deps).
	payload, isErr = s.call(4, "get_project", `{"project":"checkout-api","detail":"full"}`)
	if isErr {
		t.Fatalf("get_project errored: %s", payload)
	}
	validatePayload(t, "get_project", payload)
	var detail ProjectDetail
	if err := json.Unmarshal(payload, &detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(detail.EffectiveOwners) != 1 || detail.EffectiveOwners[0] != "group:commerce-eng" {
		t.Fatalf("expected manifest owners, got %+v", detail.EffectiveOwners)
	}
	if len(detail.Dependencies.Declared) != 1 || detail.Dependencies.Declared[0] != "money-lib" {
		t.Fatalf("expected declared dep money-lib, got %+v", detail.Dependencies)
	}

	// -- who_owns by path (§7.3: longest-prefix project, manifest source).
	payload, isErr = s.call(5, "who_owns", `{"path":"commerce/checkout/main.go"}`)
	if isErr {
		t.Fatalf("who_owns errored: %s", payload)
	}
	validatePayload(t, "who_owns", payload)
	var owners OwnersResult
	if err := json.Unmarshal(payload, &owners); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(owners.Owners) != 1 || owners.Owners[0] != "group:commerce-eng" || owners.Source != "project_manifest" {
		t.Fatalf("unexpected owners result: %+v", owners)
	}

	// -- get_affected by paths (declared-dep closure: touching money-lib
	// affects checkout-api too, §13.3).
	payload, isErr = s.call(6, "get_affected", `{"paths":["libs/money/money.go"]}`)
	if isErr {
		t.Fatalf("get_affected errored: %s", payload)
	}
	validatePayload(t, "get_affected", payload)
	var aff AffectedComputation
	if err := json.Unmarshal(payload, &aff); err != nil {
		t.Fatalf("decode: %v", err)
	}
	names := map[string]bool{}
	for _, p := range aff.Projects {
		names[p.Name] = true
	}
	if !names["money-lib"] || !names["checkout-api"] || aff.RunEverything {
		t.Fatalf("expected dep-closure affected set without run_everything, got %+v", aff)
	}

	// -- get_affected by change_id (the Change touches checkout only).
	payload, isErr = s.call(7, "get_affected", fmt.Sprintf(`{"change_id":"%s"}`, changeID))
	if isErr {
		t.Fatalf("get_affected(change) errored: %s", payload)
	}
	validatePayload(t, "get_affected", payload)

	// -- search_code (hits tagged with project id by the daemon).
	payload, isErr = s.call(8, "search_code", `{"query":"package"}`)
	if isErr {
		t.Fatalf("search_code errored: %s", payload)
	}
	validatePayload(t, "search_code", payload)
	var sc SearchCodeOutput
	if err := json.Unmarshal(payload, &sc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sc.Hits) != 2 || sc.Hits[0].ProjectID != "checkout-api" || sc.Hits[1].ProjectID != "money-lib" {
		t.Fatalf("expected project-tagged hits, got %+v", sc.Hits)
	}

	// -- search_code scoped to one project.
	payload, _ = s.call(9, "search_code", `{"query":"package","project":"money-lib"}`)
	validatePayload(t, "search_code", payload)
	sc = SearchCodeOutput{}
	if err := json.Unmarshal(payload, &sc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(sc.Hits) != 1 || sc.Hits[0].ProjectID != "money-lib" {
		t.Fatalf("expected the project filter applied, got %+v", sc.Hits)
	}

	// -- get_merge_requirements (passthrough of the daemon's own §13.5
	// shape: the seeded Change has a pending required check AND an
	// outstanding owner, so it must not be mergeable).
	payload, isErr = s.call(10, "get_merge_requirements", fmt.Sprintf(`{"change_id":"%s"}`, changeID))
	if isErr {
		t.Fatalf("get_merge_requirements errored: %s", payload)
	}
	validatePayload(t, "get_merge_requirements", payload)
	var reqs struct {
		Mergeable bool     `json:"mergeable"`
		Blockers  []string `json:"blockers"`
		Checks    struct {
			Pending []string `json:"pending"`
		} `json:"checks"`
		Owners struct {
			Outstanding []string `json:"outstanding"`
		} `json:"owners"`
	}
	if err := json.Unmarshal(payload, &reqs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if reqs.Mergeable || len(reqs.Checks.Pending) != 1 || len(reqs.Owners.Outstanding) != 1 {
		t.Fatalf("expected unit pending + owner outstanding + not mergeable, got %s", payload)
	}
}

// TestMCPErrorContract: every tool failure is the catalog's structured
// Error shape with isError set - never a bare string (§6.5, §8.3) - and
// protocol-level failures answer in-band without killing the session.
func TestMCPErrorContract(t *testing.T) {
	daemonURL, _ := startDaemon(t)
	s := startSession(t, daemonURL)

	errorSchema := compileErrorSchema(t)

	// Unknown project -> not_found, schema-shaped.
	payload, isErr := s.call(1, "get_project", `{"project":"ghost"}`)
	if !isErr {
		t.Fatalf("expected isError for an unknown project, got %s", payload)
	}
	var v interface{}
	if err := json.Unmarshal(payload, &v); err != nil {
		t.Fatalf("unmarshal error payload: %v", err)
	}
	if err := errorSchema.Validate(v); err != nil {
		t.Fatalf("error payload violates common.schema.json#/$defs/Error: %v\npayload: %s", err, payload)
	}
	var te Error
	if err := json.Unmarshal(payload, &te); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if te.Code != "not_found" || te.Retryable {
		t.Fatalf("expected non-retryable not_found, got %+v", te)
	}

	// who_owns with both path and project violates the oneOf.
	payload, isErr = s.call(2, "who_owns", `{"path":"a","project":"b"}`)
	if !isErr {
		t.Fatalf("expected isError for oneOf violation, got %s", payload)
	}
	if err := json.Unmarshal(payload, &te); err != nil || te.Code != "validation_failed" {
		t.Fatalf("expected validation_failed, got %+v (%v)", te, err)
	}

	// who_owns on an unowned path names the fail-closed rule.
	payload, isErr = s.call(3, "who_owns", `{"path":"orphan/file.go"}`)
	if !isErr {
		t.Fatalf("expected isError for an unowned path, got %s", payload)
	}
	if err := json.Unmarshal(payload, &te); err != nil || te.Code != "not_found" || te.Suggestion == "" {
		t.Fatalf("expected not_found with a suggestion, got %+v (%v)", te, err)
	}

	// Unknown Change -> the daemon's 404 surfaces as not_found.
	payload, isErr = s.call(4, "get_merge_requirements", `{"change_id":"Ideadbeef"}`)
	if !isErr {
		t.Fatalf("expected isError for an unknown change, got %s", payload)
	}

	// Unknown method -> -32601; the session keeps serving afterwards.
	s.send(`{"jsonrpc":"2.0","id":5,"method":"resources/list"}`)
	reply := s.recv()
	if reply.Error == nil || reply.Error.Code != -32601 {
		t.Fatalf("expected -32601 for an unknown method, got %+v", reply)
	}

	// Malformed JSON -> -32700; still alive after that too.
	s.send(`{this is not json`)
	reply = s.recv()
	if reply.Error == nil || reply.Error.Code != -32700 {
		t.Fatalf("expected -32700 for a parse error, got %+v", reply)
	}
	s.send(`{"jsonrpc":"2.0","id":6,"method":"ping"}`)
	if reply = s.recv(); reply.Error != nil || string(reply.ID) != "6" {
		t.Fatalf("expected the session to survive bad input, got %+v", reply)
	}
}

func compileErrorSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "docs", "spec", "mcp-tools", "common.schema.json"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	sch, err := newSchemaCompiler(t).Compile(abs + "#/$defs/Error")
	if err != nil {
		t.Fatalf("compile Error schema: %v", err)
	}
	return sch
}

// TestMCPPagination pins the adapter-level windowing (§17.4's compact-list
// defaults): the daemon's endpoints aren't paginated, so page_size/
// page_token are honored here - a truncated list always carries a
// next_page_token rather than silently dropping the tail.
func TestMCPPagination(t *testing.T) {
	daemonURL, _ := startDaemon(t)
	s := startSession(t, daemonURL)

	payload, isErr := s.call(1, "list_projects", `{"page_size":1}`)
	if isErr {
		t.Fatalf("list_projects errored: %s", payload)
	}
	validatePayload(t, "list_projects", payload)
	var page1 ListProjectsOutput
	if err := json.Unmarshal(payload, &page1); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page1.Projects) != 1 || page1.Projects[0].Name != "checkout-api" || page1.NextPageToken == "" {
		t.Fatalf("expected a 1-project page with a continuation token, got %+v", page1)
	}

	payload, _ = s.call(2, "list_projects", fmt.Sprintf(`{"page_size":1,"page_token":"%s"}`, page1.NextPageToken))
	var page2 ListProjectsOutput
	if err := json.Unmarshal(payload, &page2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page2.Projects) != 1 || page2.Projects[0].Name != "money-lib" || page2.NextPageToken != "" {
		t.Fatalf("expected the final page with no token, got %+v", page2)
	}

	// A garbage token is a structured validation error, not a crash or a
	// silent restart from page one.
	payload, isErr = s.call(3, "list_projects", `{"page_token":"garbage"}`)
	if !isErr {
		t.Fatalf("expected isError for a bad page_token, got %s", payload)
	}
	var te Error
	if err := json.Unmarshal(payload, &te); err != nil || te.Code != "validation_failed" || te.Field != "page_token" {
		t.Fatalf("expected validation_failed on page_token, got %+v (%v)", te, err)
	}

	// The query filter matches name OR path substrings.
	payload, _ = s.call(4, "list_projects", `{"query":"libs/"}`)
	var filtered ListProjectsOutput
	if err := json.Unmarshal(payload, &filtered); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(filtered.Projects) != 1 || filtered.Projects[0].Name != "money-lib" {
		t.Fatalf("expected the path filter to match only money-lib, got %+v", filtered.Projects)
	}
}
