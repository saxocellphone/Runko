package checks

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// compileSchema compiles a schema file (optionally a "#/$defs/X" sub-schema)
// from docs/spec, relative to this package's directory - the DAG's "contract
// tests against docs/spec/ schemas" bar for stage 8 (§28.3).
func compileSchema(t *testing.T, relPath string) *jsonschema.Schema {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "docs", "spec", relPath))
	if err != nil {
		t.Fatalf("resolve schema path: %v", err)
	}
	c := jsonschema.NewCompiler()
	c.Draft = jsonschema.Draft2020
	sch, err := c.Compile(abs)
	if err != nil {
		t.Fatalf("compile schema %s: %v", relPath, err)
	}
	return sch
}

func validateJSON(t *testing.T, sch *jsonschema.Schema, payload []byte) error {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal(payload, &v); err != nil {
		t.Fatalf("unmarshal payload for validation: %v", err)
	}
	return sch.Validate(v)
}

func TestWebhookEnvelopeMatchesSchema(t *testing.T) {
	sch := compileSchema(t, "webhooks/webhook-envelope.schema.json")

	env := WebhookEnvelope{
		SpecVersion: "1",
		DeliveryID:  "3fa85f64-5717-4562-b3fc-2c963f66afa6",
		Type:        "change.updated",
		OccurredAt:  time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		OrgID:       "org_1",
		MonorepoID:  "repo_1",
		Change: WebhookChange{
			ID: "chg_1042", Number: 1042, URL: "https://runko.example/changes/1042",
			State: "open", BaseSHA: "abc123", HeadSHA: "def456", GitRef: "refs/changes/1042/head",
			Title: "Reject invalid SKUs", Actor: WebhookActor{Type: "user", ID: "user_1"},
		},
		Affected: &WebhookAffected{
			ComputationID: "aff_1",
			Projects:      []WebhookAffectedProject{{ID: "prj_1", Name: "checkout-api", Path: "commerce/checkout"}},
			Paths:         []string{"commerce/checkout/handler.go"},
			ReasonCodes:   []string{"direct_path"},
			RunEverything: false,
		},
		ChecksExpected: []string{"unit", "lint"},
		API: WebhookAPI{
			ChangeURL:   "https://runko.example/api/changes/chg_1042",
			AffectedURL: "https://runko.example/api/changes/chg_1042/affected",
			ChecksURL:   "https://runko.example/api/changes/chg_1042/checks",
		},
	}

	payload, err := MarshalEnvelope(env)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}
	if err := validateJSON(t, sch, payload); err != nil {
		t.Fatalf("change.updated envelope failed schema validation: %v\npayload: %s", err, payload)
	}
}

func TestWebhookRerunEnvelopeRequiresRerunField(t *testing.T) {
	sch := compileSchema(t, "webhooks/webhook-envelope.schema.json")

	base := func() WebhookEnvelope {
		return WebhookEnvelope{
			SpecVersion: "1", DeliveryID: "3fa85f64-5717-4562-b3fc-2c963f66afa6",
			Type: "change.check_rerun_requested", OccurredAt: time.Now().UTC(),
			OrgID: "org_1", MonorepoID: "repo_1",
			Change: WebhookChange{
				ID: "chg_1", Number: 1, URL: "https://x/changes/1", State: "open",
				BaseSHA: "a", HeadSHA: "b", GitRef: "refs/changes/1/head", Title: "t",
				Actor: WebhookActor{Type: "user", ID: "u1"},
			},
			API: WebhookAPI{ChangeURL: "https://x", AffectedURL: "https://x", ChecksURL: "https://x"},
		}
	}

	missingRerun := base()
	payload, err := MarshalEnvelope(missingRerun)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}
	if err := validateJSON(t, sch, payload); err == nil {
		t.Fatalf("expected schema to reject change.check_rerun_requested without a rerun field")
	}

	withRerun := base()
	withRerun.Rerun = &WebhookRerun{CheckName: "unit", RequestedBy: WebhookActor{Type: "user", ID: "u1"}}
	payload2, err := MarshalEnvelope(withRerun)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}
	if err := validateJSON(t, sch, payload2); err != nil {
		t.Fatalf("expected schema to accept a rerun envelope with the rerun field populated: %v\npayload: %s", err, payload2)
	}
}

func TestWebhookEnvelopeRejectsUnknownType(t *testing.T) {
	sch := compileSchema(t, "webhooks/webhook-envelope.schema.json")
	env := WebhookEnvelope{
		SpecVersion: "1", DeliveryID: "3fa85f64-5717-4562-b3fc-2c963f66afa6",
		Type: "change.exploded", OccurredAt: time.Now().UTC(),
		OrgID: "org_1", MonorepoID: "repo_1",
		Change: WebhookChange{
			ID: "chg_1", Number: 1, URL: "https://x", State: "open",
			BaseSHA: "a", HeadSHA: "b", GitRef: "refs/changes/1/head", Title: "t",
			Actor: WebhookActor{Type: "user", ID: "u1"},
		},
		API: WebhookAPI{ChangeURL: "https://x", AffectedURL: "https://x", ChecksURL: "https://x"},
	}
	payload, err := MarshalEnvelope(env)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}
	if err := validateJSON(t, sch, payload); err == nil {
		t.Fatalf("expected schema to reject an unrecognized event type")
	}
}

func TestCheckRunMatchesSchema(t *testing.T) {
	sch := compileSchema(t, "webhooks/checkrun.schema.json#/$defs/CheckRun")

	completed := `{"name":"unit","external_id":"job-1","status":"completed","conclusion":"success","reporter":"github-actions","completed_at":"2026-01-01T00:00:00Z"}`
	if err := validateJSON(t, sch, []byte(completed)); err != nil {
		t.Fatalf("expected a completed CheckRun with a conclusion to validate: %v", err)
	}

	incomplete := `{"name":"unit","external_id":"job-1","status":"completed","reporter":"github-actions"}`
	if err := validateJSON(t, sch, []byte(incomplete)); err == nil {
		t.Fatalf("expected schema to reject a completed CheckRun missing conclusion/completed_at")
	}

	queued := `{"name":"unit","external_id":"job-1","status":"queued","reporter":"github-actions"}`
	if err := validateJSON(t, sch, []byte(queued)); err != nil {
		t.Fatalf("expected a queued CheckRun without a conclusion to validate: %v", err)
	}
}

func TestMergeRequirementsMatchesCommonSchema(t *testing.T) {
	sch := compileSchema(t, "mcp-tools/common.schema.json#/$defs/MergeRequirements")

	req := ComputeMergeRequirements(
		"chg_1042",
		[]OwnerRequirement{{OwnerRef: "group:commerce-eng", Satisfied: true}},
		[]string{"lint"},
		[]CheckRunView{{Name: "lint", Status: CheckStatusCompleted, Conclusion: ConclusionSuccess}},
		nil, nil, nil,
	)
	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal MergeRequirements: %v", err)
	}
	if err := validateJSON(t, sch, payload); err != nil {
		t.Fatalf("MergeRequirements failed schema validation: %v\npayload: %s", err, payload)
	}

	// An empty (trivially mergeable) Change must also validate - the wire
	// marshaler must emit [] rather than null for empty slices.
	empty := ComputeMergeRequirements("chg_2", nil, nil, nil, nil, nil, nil)
	emptyPayload, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal empty MergeRequirements: %v", err)
	}
	if err := validateJSON(t, sch, emptyPayload); err != nil {
		t.Fatalf("empty MergeRequirements failed schema validation: %v\npayload: %s", err, emptyPayload)
	}
}
