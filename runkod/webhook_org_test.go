package runkod

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/saxocellphone/runko/internal/gitfixture"
)

// orgIDOf unmarshals just the envelope's org_id from an outbox payload.
func orgIDOf(t *testing.T, payload []byte) string {
	t.Helper()
	var env struct {
		OrgID string `json:"org_id"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return env.OrgID
}

func dueWebhookPayloads(t *testing.T, store Store) [][]byte {
	t.Helper()
	due, err := store.ListDueWebhookDeliveries(context.Background(), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ListDueWebhookDeliveries: %v", err)
	}
	payloads := make([][]byte, len(due))
	for i, d := range due {
		payloads[i] = d.Payload
	}
	return payloads
}

// With one daemon-wide --webhook-url shared by every org's OutboxWorker, an
// envelope without org_id is unattributable (docs/migration-findings.md
// #13) - the funnel's change.updated must carry the processor's org.
func TestFunnelWebhookEnvelopeCarriesOrgID(t *testing.T) {
	bare := newBareRepo(t)
	repo := gitfixture.New(t)
	repo.WriteFile("README.md", "hi\n")
	repo.Commit("initial")
	oldSHA, _ := pushCommit(t, repo, bare, "refs/heads/main")

	repo.WriteFile("feature.txt", "v1\n")
	repo.Commit("add a feature")
	_, headSHA := pushCommit(t, repo, bare, "refs/for/main")

	store := NewMemStore()
	p := newTestProcessor(bare, store)
	p.OrgName = "acme"
	if result := p.Process(context.Background(), RefUpdate{OldSHA: oldSHA, NewSHA: headSHA, Ref: "refs/for/main"}, nil); !result.Accepted {
		t.Fatalf("push not accepted: %+v", result)
	}

	payloads := dueWebhookPayloads(t, store)
	if len(payloads) == 0 {
		t.Fatalf("expected a change.updated webhook enqueued")
	}
	for _, payload := range payloads {
		if got := orgIDOf(t, payload); got != "acme" {
			t.Fatalf("change.updated org_id: want acme, got %q (payload %s)", got, payload)
		}
	}
}

func TestLandedAndRerunWebhooksCarryOrgID(t *testing.T) {
	store := NewMemStore()
	s := &Server{Store: store, SettingsOrg: "acme"}
	change := Change{ChangeKey: "Iabc", State: "open", BaseSHA: "b", HeadSHA: "h", GitRef: "refs/changes/Iabc/head", Title: "t"}

	s.enqueueLandedWebhook(context.Background(), change, "landedsha")
	s.enqueueRerunWebhook(context.Background(), change, "unit", "user:val")

	payloads := dueWebhookPayloads(t, store)
	if len(payloads) != 2 {
		t.Fatalf("expected 2 enqueued webhooks, got %d", len(payloads))
	}
	for _, payload := range payloads {
		if got := orgIDOf(t, payload); got != "acme" {
			t.Fatalf("org_id: want acme, got %q (payload %s)", got, payload)
		}
	}
}
