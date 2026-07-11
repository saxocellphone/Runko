package runkod

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/saxocellphone/runko/internal/gitfixture"
	"github.com/saxocellphone/runko/platform/receive"
)

// TestGateRunWhenDirectSkipsClosureDependents is §14.5.9 at the merge
// gate - the lockstep proof: a change touching only the dependency
// requires the dependent's affected-class lane but NOT its direct-only
// unit lane, so the gate never demands a check the executor's matrix
// won't run.
func TestGateRunWhenDirectSkipsClosureDependents(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("lib/PROJECT.yaml",
		"schema: project/v1\nname: lib\ntype: library\nowners:\n  - group:eng\nci:\n  checks:\n    - name: lib-test\n      command: go test ./lib/...\n")
	repo.WriteFile("svc/PROJECT.yaml",
		"schema: project/v1\nname: svc\ntype: service\ndependencies:\n  - lib\nci:\n  checks:\n    - name: svc-integration\n      command: go test ./svc/e2e/...\n    - name: svc-unit\n      command: go test ./svc/...\n      run_when: direct\n")
	repo.Commit("initial")
	pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("lib/lib.go", "package lib\n")
	repo.Commit("touch lib\n\nChange-Id: I0123456789abcdef0123456789abcdef01234567")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	processor := &Processor{RepoDir: bare, TrunkRef: "main", Scanner: receive.NoOpScanner{}, Store: store}
	result := processor.Process(context.Background(), RefUpdate{OldSHA: zeroOID, NewSHA: headSHA, Ref: "refs/for/main"}, nil)
	if !result.Accepted {
		t.Fatalf("seed push was rejected: %+v", result)
	}

	server := &Server{RepoDir: bare, TrunkRef: "main", Store: store, Processor: processor, Token: "sekret"}
	handler, err := server.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	reqs := getMergeRequirements(t, srv, result.ChangeID)
	required := map[string]bool{}
	for _, name := range reqs.RequiredChecks {
		required[name] = true
	}
	if !required["lib-test"] || !required["svc-integration"] {
		t.Fatalf("the gate must require the touched project's checks and the dependent's affected lane, got %v", reqs.RequiredChecks)
	}
	if required["svc-unit"] {
		t.Fatalf("the gate must NOT require the dependent's direct-only unit lane (it would deadlock: the executor won't run it), got %v", reqs.RequiredChecks)
	}
}
