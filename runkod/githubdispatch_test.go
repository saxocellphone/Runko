package runkod

// Native Mode C dispatch tests (2026-07-17, runkod/README.md): the
// envelope -> repository_dispatch contract the bridge used to own, now
// driven through the outbox worker against a stub GitHub. The payload
// shape assertions ARE the compatibility contract - the runko-checks
// workflow must not notice the shim being replaced.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/saxocellphone/runko/platform/checks"
)

func dispatchEnvelope(t *testing.T, typ, deliveryID string) []byte {
	t.Helper()
	env := checks.WebhookEnvelope{
		SpecVersion: "1",
		DeliveryID:  deliveryID,
		Type:        typ,
		OccurredAt:  time.Now(),
		OrgID:       "acme",
		Change: checks.WebhookChange{
			ID: "Iabc", State: "open", BaseSHA: "base", HeadSHA: "head",
			GitRef: "refs/changes/Iabc/head", Title: "t",
			Actor: checks.WebhookActor{Type: "user", ID: "saxo"},
		},
		Affected: &checks.WebhookAffected{
			ComputationID: "aff_1",
			Projects: []checks.WebhookAffectedProject{
				{Name: "platform", Path: ""}, {Name: "web", Path: "web"},
			},
		},
	}
	payload, err := checks.MarshalEnvelope(env)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}
	return payload
}

// dispatcherFixture: a wired org ("acme" -> acme/monorepo), a stub GitHub
// capturing dispatches, and a dispatcher minting a static token.
func dispatcherFixture(t *testing.T) (*GithubDispatcher, *MemStore, *httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var calls atomic.Int32
	var lastReq atomic.Value // map: auth header + body
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/monorepo/dispatches" {
			t.Errorf("unexpected github path: %s", r.URL.Path)
		}
		calls.Add(1)
		buf := make([]byte, r.ContentLength)
		r.Body.Read(buf)
		lastReq.Store(map[string]string{"auth": r.Header.Get("Authorization"), "body": string(buf)})
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(gh.Close)

	store := NewMemStore()
	settings, _ := store.GetOrgSettings(context.Background(), "acme")
	settings.GithubMirrorRepo = "acme/monorepo"
	if err := store.UpdateOrgSettings(context.Background(), "acme", settings); err != nil {
		t.Fatalf("UpdateOrgSettings: %v", err)
	}
	g := &GithubDispatcher{
		Directory: store,
		OrgName:   "acme",
		Token:     func(ctx context.Context, repo string) (string, error) { return "ghs_minted", nil },
		APIBase:   gh.URL,
	}
	return g, store, gh, &calls, &lastReq
}

func TestGithubDispatchThroughOutbox(t *testing.T) {
	g, store, _, calls, lastReq := dispatcherFixture(t)
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, "change.updated", dispatchEnvelope(t, "change.updated", "d1")); err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}

	// URL-less worker: dispatch is the only delivery target.
	worker := &OutboxWorker{Store: store, GithubDispatch: g}
	if n, err := worker.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce: n=%d err=%v", n, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("want 1 dispatch, got %d", calls.Load())
	}
	got := lastReq.Load().(map[string]string)
	if got["auth"] != "Bearer ghs_minted" {
		t.Fatalf("dispatch auth: %q", got["auth"])
	}
	var body struct {
		EventType     string `json:"event_type"`
		ClientPayload struct {
			ChangeID         string   `json:"change_id"`
			HeadSHA          string   `json:"head_sha"`
			GitRef           string   `json:"git_ref"`
			Trigger          string   `json:"trigger"`
			DeliveryID       string   `json:"delivery_id"`
			AffectedProjects []string `json:"affected_projects"`
		} `json:"client_payload"`
	}
	if err := json.Unmarshal([]byte(got["body"]), &body); err != nil {
		t.Fatalf("dispatch body: %v", err)
	}
	cp := body.ClientPayload
	if body.EventType != "runko-change" || cp.ChangeID != "Iabc" || cp.HeadSHA != "head" ||
		cp.GitRef != "refs/changes/Iabc/head" || cp.Trigger != "change.updated" || cp.DeliveryID != "d1" ||
		len(cp.AffectedProjects) != 2 || cp.AffectedProjects[0] != "platform" {
		t.Fatalf("dispatch payload (the workflow's wire contract): %+v", body)
	}

	// Delivered rows are done - no redelivery.
	if due, _ := store.ListDueWebhookDeliveries(ctx, time.Now().Add(time.Hour)); len(due) != 0 {
		t.Fatalf("delivered dispatch still due: %+v", due)
	}
}

func TestGithubDispatchSkipsUninterestingAndUnwired(t *testing.T) {
	g, store, _, calls, _ := dispatcherFixture(t)
	ctx := context.Background()

	// change.landed never triggers CI; it must succeed as a no-op.
	if _, err := store.EnqueueWebhook(ctx, "change.landed", dispatchEnvelope(t, "change.landed", "d2")); err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}
	worker := &OutboxWorker{Store: store, GithubDispatch: g}
	if n, err := worker.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce: n=%d err=%v", n, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("change.landed must not dispatch, got %d calls", calls.Load())
	}

	// An org with no wiring: delivered as a no-op, never retried.
	settings, _ := store.GetOrgSettings(ctx, "acme")
	settings.GithubMirrorRepo = ""
	store.UpdateOrgSettings(ctx, "acme", settings)
	if _, err := store.EnqueueWebhook(ctx, "change.updated", dispatchEnvelope(t, "change.updated", "d3")); err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}
	if n, err := worker.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce: n=%d err=%v", n, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("unwired org must not dispatch, got %d calls", calls.Load())
	}
	if due, _ := store.ListDueWebhookDeliveries(ctx, time.Now().Add(time.Hour)); len(due) != 0 {
		t.Fatalf("no-op deliveries must be marked delivered: %+v", due)
	}
}

func TestGithubDispatchFailureRidesOutboxBackoff(t *testing.T) {
	g, store, gh, _, _ := dispatcherFixture(t)
	gh.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusBadGateway)
	})
	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, "change.updated", dispatchEnvelope(t, "change.updated", "d4")); err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}
	worker := &OutboxWorker{Store: store, GithubDispatch: g}
	if n, err := worker.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce: n=%d err=%v", n, err)
	}
	// The failed dispatch must be retried after backoff - not marked done.
	if due, _ := store.ListDueWebhookDeliveries(ctx, time.Now().Add(time.Hour)); len(due) != 1 {
		t.Fatalf("failed dispatch must stay due for retry, got %+v", due)
	}
}

func TestGithubDispatchAfterURLDelivery(t *testing.T) {
	g, store, _, calls, _ := dispatcherFixture(t)
	var urlHits atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urlHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()

	ctx := context.Background()
	if _, err := store.EnqueueWebhook(ctx, "change.updated", dispatchEnvelope(t, "change.updated", "d5")); err != nil {
		t.Fatalf("EnqueueWebhook: %v", err)
	}
	worker := &OutboxWorker{Store: store, URL: receiver.URL, Secret: []byte("shh"), GithubDispatch: g}
	if n, err := worker.RunOnce(ctx); err != nil || n != 1 {
		t.Fatalf("RunOnce: n=%d err=%v", n, err)
	}
	if urlHits.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("both targets must fire: url=%d dispatch=%d", urlHits.Load(), calls.Load())
	}
}
