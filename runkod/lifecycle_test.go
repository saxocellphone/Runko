package runkod

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/saxocellphone/runko/checks"
)

// Stage 12c-③ surface tests: list/abandon/rerun + check staleness. All run
// against newTestServer's fixture (one Change on checkout-api, which
// declares ci.checks: unit - so "unit" is a real required check here).

func authedPost(t *testing.T, srv *httptest.Server, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func TestListChangesFiltersByState(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()

	decode := func(resp *http.Response) []Change {
		t.Helper()
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("list: %d: %s", resp.StatusCode, body)
		}
		var list []Change
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return list
	}

	open := decode(authedGet(t, srv, "/api/changes?state=open", "sekret"))
	if len(open) != 1 || open[0].ChangeKey != changeID {
		t.Fatalf("expected the seeded open change, got %+v", open)
	}
	if landed := decode(authedGet(t, srv, "/api/changes?state=landed", "sekret")); len(landed) != 0 {
		t.Fatalf("expected no landed changes, got %+v", landed)
	}
	if all := decode(authedGet(t, srv, "/api/changes", "sekret")); len(all) != 1 {
		t.Fatalf("expected one change without a filter, got %+v", all)
	}

	resp := authedGet(t, srv, "/api/changes?state=bogus", "sekret")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a bogus state, got %d", resp.StatusCode)
	}
}

func TestAbandonReopenAndLandRefusal(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()

	// Abandon: 200, state flips.
	resp := authedPost(t, srv, "/api/changes/"+changeID+"/abandon", "sekret")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("abandon: %d: %s", resp.StatusCode, body)
	}
	var change Change
	if err := json.NewDecoder(resp.Body).Decode(&change); err != nil || change.State != "abandoned" {
		t.Fatalf("expected abandoned, got %+v (err %v)", change, err)
	}

	// Idempotent second abandon.
	resp2 := authedPost(t, srv, "/api/changes/"+changeID+"/abandon", "sekret")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected idempotent abandon to return 200, got %d", resp2.StatusCode)
	}

	// Landing an abandoned Change is refused regardless of its gates.
	landResp := authedPost(t, srv, "/api/changes/"+changeID+"/land", "sekret")
	defer landResp.Body.Close()
	if landResp.StatusCode != http.StatusConflict {
		body, _ := io.ReadAll(landResp.Body)
		t.Fatalf("expected 409 landing an abandoned change, got %d: %s", landResp.StatusCode, body)
	}
	body, _ := io.ReadAll(landResp.Body)
	if !strings.Contains(string(body), "invalid_state") {
		t.Fatalf("expected invalid_state, got %s", body)
	}
}

func TestRepushReopensAbandonedChange(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "b1", "h1", "refs/changes/1/head", "t", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.MarkChangeAbandoned(ctx, "Iabc"); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	reopened, err := store.CreateOrUpdateChange(ctx, "Iabc", "b1", "h2", "refs/changes/1/head", "t", "")
	if err != nil {
		t.Fatalf("re-push: %v", err)
	}
	if reopened.State != "open" {
		t.Fatalf("expected the re-push to reopen (§7.4, change.reopened), got %+v", reopened)
	}
}

func TestAbandonLandedChangeRefused(t *testing.T) {
	store := NewMemStore()
	ctx := context.Background()
	if _, err := store.CreateOrUpdateChange(ctx, "Iabc", "b1", "h1", "refs/changes/1/head", "t", ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.MarkChangeLanded(ctx, "Iabc", "h1", ""); err != nil {
		t.Fatalf("land: %v", err)
	}
	if _, err := store.MarkChangeAbandoned(ctx, "Iabc"); err == nil {
		t.Fatalf("expected abandoning a landed change to error - landed is terminal")
	}
}

// TestRerunCheckResetsGateAndEmitsWebhook: a green required check goes back
// to pending, and the org's CI plugin gets change.check_rerun_requested
// with the rerun block (§14.4.2 - schema and RerunCheck existed since
// stage 8; this is their first wire-level caller).
func TestRerunCheckResetsGateAndEmitsWebhook(t *testing.T) {
	srv, changeID := newTestServer(t)
	defer srv.Close()

	// Report "unit" green.
	body := strings.NewReader(`{"name":"unit","external_id":"job-1","status":"completed","conclusion":"success","reporter":"ci"}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+changeID+"/checks", body)
	req.Header.Set("Authorization", "Bearer sekret")
	req.Header.Set("Content-Type", "application/json")
	if resp, err := srv.Client().Do(req); err != nil || resp.StatusCode >= 300 {
		t.Fatalf("report check: %v (%v)", err, resp)
	}

	// Rerun: 200, refreshed requirements show unit pending again.
	resp := authedPost(t, srv, "/api/changes/"+changeID+"/checks/unit/rerun", "sekret")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("rerun: %d: %s", resp.StatusCode, b)
	}
	var reqs checks.MergeRequirements
	if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
		t.Fatalf("decode requirements: %v", err)
	}
	if len(reqs.PendingChecks) != 1 || reqs.PendingChecks[0] != "unit" || reqs.Mergeable {
		t.Fatalf("expected unit pending and not mergeable after rerun, got %+v", reqs)
	}

	// Unknown check name: structured 400 naming what IS required.
	resp2 := authedPost(t, srv, "/api/changes/"+changeID+"/checks/nonsense/rerun", "sekret")
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-required check, got %d", resp2.StatusCode)
	}
	b, _ := io.ReadAll(resp2.Body)
	if !strings.Contains(string(b), "unknown_check") || !strings.Contains(string(b), "unit") {
		t.Fatalf("expected unknown_check naming the required set, got %s", b)
	}
}

// TestRerunWebhookEnqueued asserts the outbox actually receives the
// change.check_rerun_requested envelope with its rerun block - the schema
// existed since stage 8 with no producer; this pins that the endpoint IS
// one now.
func TestRerunWebhookEnqueued(t *testing.T) {
	srv, _, changeID, store := newPolicyGateServer(t,
		"schema: project/v1\nname: checkout-api\ntype: service\nci:\n  checks:\n    - name: unit\n      command: go test ./...\n", nil)
	defer srv.Close()

	resp := authedPost(t, srv, "/api/changes/"+changeID+"/checks/unit/rerun", "sekret")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("rerun: %d", resp.StatusCode)
	}

	due, err := store.ListDueWebhookDeliveries(context.Background(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries: %v", err)
	}
	var payload []byte
	for _, d := range due {
		if d.EventType == "change.check_rerun_requested" {
			payload = d.Payload
		}
	}
	if payload == nil {
		t.Fatalf("expected a change.check_rerun_requested delivery in the outbox, got %+v", due)
	}
	var env struct {
		Rerun struct {
			CheckName   string `json:"check_name"`
			RequestedBy struct {
				ID string `json:"id"`
			} `json:"requested_by"`
		} `json:"rerun"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if env.Rerun.CheckName != "unit" || env.Rerun.RequestedBy.ID == "" {
		t.Fatalf("expected the rerun block naming unit + a requester, got %+v", env.Rerun)
	}
}

// TestStaleRequiredCheckBlocksLoudly: §14.4.2's TTL, consulted for the
// first time (stage 12c-③), over the wire: a required run reported
// in_progress and then untouched past its TTL gets a loud blocker naming
// it in merge requirements. Both clocks are injected: the MemStore stamps
// the run at `reported`, the Server evaluates staleness at `evaluated`.
func TestStaleRequiredCheckBlocksLoudly(t *testing.T) {
	reported := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	evaluated := reported.Add(48 * time.Hour) // default TTL is 24h

	srv, _, changeID, store := newPolicyGateServer(t,
		"schema: project/v1\nname: checkout-api\ntype: service\nci:\n  checks:\n    - name: unit\n      command: go test ./...\n",
		func(server *Server) { server.Now = func() time.Time { return evaluated } })
	defer srv.Close()
	store.(*MemStore).Now = func() time.Time { return reported }

	// Report unit in_progress (stamped at `reported`).
	body := strings.NewReader(`{"name":"unit","external_id":"job-1","status":"in_progress","reporter":"ci"}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/changes/"+changeID+"/checks", body)
	req.Header.Set("Authorization", "Bearer sekret")
	req.Header.Set("Content-Type", "application/json")
	if resp, err := srv.Client().Do(req); err != nil || resp.StatusCode >= 300 {
		t.Fatalf("report check: %v (%+v)", err, resp)
	}

	reqs := getMergeRequirements(t, srv, changeID)
	if reqs.Mergeable {
		t.Fatalf("expected not mergeable, got %+v", reqs)
	}
	staleBlocker := ""
	for _, b := range reqs.Blockers {
		if strings.Contains(b, "stale") {
			staleBlocker = b
		}
	}
	if !strings.Contains(staleBlocker, "unit") {
		t.Fatalf("expected a stale blocker naming unit after 48h, got %v", reqs.Blockers)
	}

	// A fresh report is NOT stale: same wire path, Server.Now just past it.
	server2, _, changeID2, store2 := newPolicyGateServer(t,
		"schema: project/v1\nname: checkout-api\ntype: service\nci:\n  checks:\n    - name: unit\n      command: go test ./...\n",
		func(server *Server) { server.Now = func() time.Time { return reported.Add(time.Minute) } })
	defer server2.Close()
	store2.(*MemStore).Now = func() time.Time { return reported }
	body2 := strings.NewReader(`{"name":"unit","external_id":"job-1","status":"in_progress","reporter":"ci"}`)
	req2, _ := http.NewRequest(http.MethodPost, server2.URL+"/api/changes/"+changeID2+"/checks", body2)
	req2.Header.Set("Authorization", "Bearer sekret")
	req2.Header.Set("Content-Type", "application/json")
	if resp, err := server2.Client().Do(req2); err != nil || resp.StatusCode >= 300 {
		t.Fatalf("report check: %v (%+v)", err, resp)
	}
	reqs2 := getMergeRequirements(t, server2, changeID2)
	for _, b := range reqs2.Blockers {
		if strings.Contains(b, "stale") {
			t.Fatalf("did not expect a stale blocker one minute in, got %v", reqs2.Blockers)
		}
	}
}
