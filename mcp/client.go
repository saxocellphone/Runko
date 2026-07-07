package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/saxocellphone/runko/affected"
	"github.com/saxocellphone/runko/index"
	"github.com/saxocellphone/runko/search"
)

// Error is the canonical structured tool-call error,
// docs/spec/mcp-tools/common.schema.json#/$defs/Error - every tool failure
// this adapter reports uses this shape, never a bare string (§6.5, §8.3).
// Agents should branch on Code, not parse Message.
type Error struct {
	Code       string `json:"code"`
	Field      string `json:"field,omitempty"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
	DocURL     string `json:"doc_url,omitempty"`
	Retryable  bool   `json:"retryable"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Client is the thin HTTP client against runkod's REST API - the same
// handlers the CLI and every other client use (§8.3: MCP is a remote
// adapter over the one contract, not a second backend).
type Client struct {
	BaseURL string // e.g. "http://runkod.internal:8080", no trailing slash needed
	Token   string // deploy token (Bearer) - same v1 trust boundary as the CLI
	HTTP    *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// get performs an authed GET and decodes a 2xx body into out (skipped when
// out is nil). Non-2xx responses become *Error: runkod's own structured
// errors (internal/clierr.Error, capitalized field names on the wire) are
// re-shaped into the schema's lowercase Error; anything else gets a
// generic code with Retryable set by status class.
func (c *Client) get(ctx context.Context, path string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return &Error{Code: "validation_failed", Message: err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return &Error{Code: "backend_unavailable", Message: fmt.Sprintf("contact runkod: %v", err), Retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return errorFromResponse(resp)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return &Error{Code: "backend_error", Message: fmt.Sprintf("decode runkod response: %v", err), Retryable: true}
	}
	return nil
}

func errorFromResponse(resp *http.Response) *Error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	// runkod's structured errors serialize internal/clierr.Error with Go's
	// capitalized field names (no json tags on that type).
	var ce struct{ Code, Field, Message, Suggestion, DocURL string }
	if json.Unmarshal(body, &ce) == nil && ce.Code != "" {
		return &Error{
			Code: ce.Code, Field: ce.Field, Message: ce.Message,
			Suggestion: ce.Suggestion, DocURL: ce.DocURL,
			Retryable: resp.StatusCode >= 500,
		}
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = resp.Status
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return &Error{Code: "not_found", Message: msg}
	case resp.StatusCode >= 500:
		return &Error{Code: "backend_error", Message: msg, Retryable: true}
	default:
		return &Error{Code: "validation_failed", Message: msg}
	}
}

// ListProjects fetches the trunk-tip project index (GET /api/projects).
func (c *Client) ListProjects(ctx context.Context) ([]index.IndexedProject, error) {
	var projects []index.IndexedProject
	if err := c.get(ctx, "/api/projects", &projects); err != nil {
		return nil, err
	}
	return projects, nil
}

// AffectedByPaths computes affected projects for a path set at trunk tip
// (GET /api/affected?paths=...).
func (c *Client) AffectedByPaths(ctx context.Context, paths []string) (affected.Result, error) {
	var result affected.Result
	q := url.Values{"paths": {strings.Join(paths, ",")}}
	if err := c.get(ctx, "/api/affected?"+q.Encode(), &result); err != nil {
		return affected.Result{}, err
	}
	return result, nil
}

// AffectedByChange re-fetches the affected computation for a Change
// (GET /api/changes/{key}/affected).
func (c *Client) AffectedByChange(ctx context.Context, changeID string) (affected.Result, error) {
	var result affected.Result
	if err := c.get(ctx, "/api/changes/"+url.PathEscape(changeID)+"/affected", &result); err != nil {
		return affected.Result{}, err
	}
	return result, nil
}

// MergeRequirements fetches a Change's merge requirements raw
// (GET /api/changes/{key}/merge-requirements). The daemon already emits
// the schema's exact nested shape (checks.MergeRequirements.MarshalJSON),
// so this passes it through untouched rather than round-tripping it
// through Go types that could drift.
func (c *Client) MergeRequirements(ctx context.Context, changeID string) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := c.get(ctx, "/api/changes/"+url.PathEscape(changeID)+"/merge-requirements", &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// Search runs a code search (GET /api/search?q=...&num=...). Hits arrive
// already project-tagged by the daemon's longest-prefix rule.
func (c *Client) Search(ctx context.Context, query string, num int) (search.Result, error) {
	q := url.Values{"q": {query}}
	if num > 0 {
		q.Set("num", strconv.Itoa(num))
	}
	var result search.Result
	if err := c.get(ctx, "/api/search?"+q.Encode(), &result); err != nil {
		return search.Result{}, err
	}
	return result, nil
}
