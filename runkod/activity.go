package runkod

// Agent session activity ingest (§12.6.1, stage 19): the platform's one
// CLIENT-CLAIMED write path. Harness hooks report what the agent is doing
// (reads, edits, commands) into a workspace's activity feed; the rows are
// observability-only by decision - nothing may feed them into policy,
// gates, or affected computation. Ownership and auth mirror snapshot
// pushes (§12.2): owner-only, closed refuses, actor from the principal
// and never the body. Content policy: kind coerces soft (unknown ->
// "note" - telemetry never fails the work it describes), detail truncates
// to 240 runes and passes the funnel's secret scanner - a finding redacts
// that event, a scanner error redacts the whole batch (fail-closed).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/saxocellphone/runko/internal/clierr"
	"github.com/saxocellphone/runko/platform/receive"
)

const (
	// workspaceActivityDetailMax bounds detail at ingest (§12.6.1):
	// activity is metadata - a path, a command line - never content.
	workspaceActivityDetailMax = 240
	// workspaceActivityBatchMax bounds one POST; the CLI sends single
	// events today, the batch body is the future spooler's contract.
	workspaceActivityBatchMax = 100
)

type activityEventBody struct {
	Kind      string `json:"kind"`
	Detail    string `json:"detail"`
	SessionID string `json:"session_id"`
}

type recordActivityRequest struct {
	Events []activityEventBody `json:"events"`
}

// normalizeActivityKind coerces unknown kinds to "note" (§12.6.1): the
// vocabulary is deliberately soft at the edge - harness tool inventories
// change faster than the spec, and telemetry must never be rejected over
// naming.
func normalizeActivityKind(kind string) string {
	switch kind {
	case WorkspaceActivityRead, WorkspaceActivityEdit, WorkspaceActivityCommand,
		WorkspaceActivitySearch, WorkspaceActivityNote:
		return kind
	default:
		return WorkspaceActivityNote
	}
}

// truncateRunes cuts s to at most max runes - rune-safe so a multibyte
// path never splits into invalid UTF-8 at the boundary.
func truncateRunes(s string, max int) string {
	if len(s) <= max { // fast path: bytes <= max implies runes <= max
		return s
	}
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return string(rs[:max])
}

// redactActivitySecrets runs the funnel's wired secret scanner over a
// batch's detail strings (§12.6.1 content policy): command lines carry
// secrets in the wild. A finding redacts that event's detail; a scanner
// ERROR redacts the whole batch - fail-closed, the funnel's posture: a
// broken scanner becomes visibly redacted telemetry, never a stored
// secret. No Processor/Scanner wired (eval profile, tests) means no scan.
func (s *Server) redactActivitySecrets(rows []WorkspaceActivity) {
	if s.Processor == nil || s.Processor.Scanner == nil {
		return
	}
	files := make([]receive.FileContent, len(rows))
	for i, row := range rows {
		files[i] = receive.FileContent{Path: strconv.Itoa(i), Content: []byte(row.Detail)}
	}
	findings, err := s.Processor.Scanner.Scan(files)
	if err != nil {
		for i := range rows {
			rows[i].Detail = "[redacted: scan_error]"
		}
		return
	}
	for _, f := range findings {
		if i, err := strconv.Atoi(f.Path); err == nil && i >= 0 && i < len(rows) {
			rows[i].Detail = "[redacted: " + f.RuleID + "]"
		}
	}
}

// recordWorkspaceActivityCore is activity ingest's decision core (the
// actions.go split): guards, normalize, redact, store, one bus poke.
func (s *Server) recordWorkspaceActivityCore(ctx context.Context, id string, principal *Principal, events []activityEventBody) (int, *apiError) {
	ws, ok, err := s.Store.GetWorkspace(ctx, id)
	if err != nil {
		return 0, internalErr(err)
	}
	if !ok {
		return 0, typedErr(http.StatusNotFound, clierr.Error{
			Code: "workspace_not_found", Field: "id",
			Message:    fmt.Sprintf("no workspace %q is registered", id),
			Suggestion: "runko workspace list",
		})
	}
	// Owner-only for named principals (operators exempt) - the same rule
	// snapshot pushes and workspace delete enforce (§12.2); activity is
	// attributed workspace state, not a broadcast channel.
	if principal != nil && !principal.Admin && ws.Owner != "" && principal.Name != ws.Owner {
		return 0, typedErr(http.StatusForbidden, clierr.Error{
			Code: "not_workspace_owner", Field: "id",
			Message:    fmt.Sprintf("workspace %q belongs to %s (§12.2)", id, ws.Owner),
			Suggestion: "only the owner or an operator may report activity into it",
		})
	}
	if ws.Status == "closed" {
		return 0, typedErr(http.StatusConflict, clierr.Error{
			Code: "workspace_closed", Field: "id",
			Message:    fmt.Sprintf("workspace %q is closed - its task concluded (§12.2)", id),
			Suggestion: "start the new task in a fresh workspace: runko workspace create",
		})
	}
	if len(events) == 0 {
		return 0, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "missing_field", Field: "events",
			Message:    "events must contain at least one entry",
			Suggestion: `post {"events":[{"kind":"note","detail":"..."}]}`,
		})
	}
	if len(events) > workspaceActivityBatchMax {
		return 0, typedErr(http.StatusBadRequest, clierr.Error{
			Code: "batch_too_large", Field: "events",
			Message:    fmt.Sprintf("%d events exceed the %d-per-request cap (§12.6.1)", len(events), workspaceActivityBatchMax),
			Suggestion: fmt.Sprintf("split the batch into chunks of at most %d", workspaceActivityBatchMax),
		})
	}

	actor := "" // "" = the anonymous deploy token, the store.go convention
	if principal != nil {
		actor = principal.Name
	}
	rows := make([]WorkspaceActivity, 0, len(events))
	for _, ev := range events {
		rows = append(rows, WorkspaceActivity{
			WorkspaceID: id,
			Actor:       actor,
			Kind:        normalizeActivityKind(ev.Kind),
			Detail:      truncateRunes(ev.Detail, workspaceActivityDetailMax),
			SessionID:   truncateRunes(ev.SessionID, workspaceActivityDetailMax),
		})
	}
	s.redactActivitySecrets(rows)

	stored, err := s.Store.RecordWorkspaceActivity(ctx, rows)
	if err != nil {
		return 0, internalErr(err)
	}
	// One poke per accepted batch (§12.6.1): a bus-only agent_activity
	// event - never a stored timeline row (see the const's comment).
	// Watchers refetch through ListWorkspaceActivity; the bus coalesces
	// bursts and Publish never blocks.
	poke := WorkspaceEvent{Type: WorkspaceEventAgentActivity, WorkspaceID: id, Actor: actor}
	if len(stored) > 0 {
		poke.OccurredAt = stored[len(stored)-1].OccurredAt
	}
	s.Events.Publish(poke)
	return len(stored), nil
}

func (s *Server) handleRecordWorkspaceActivity(w http.ResponseWriter, r *http.Request) {
	var req recordActivityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, clierr.Error{
			Code: "invalid_body", Message: "request body must be JSON with an events array",
		})
		return
	}
	n, apiErr := s.recordWorkspaceActivityCore(r.Context(), r.PathValue("id"), s.principalFor(r), req.Events)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"recorded": n})
}
