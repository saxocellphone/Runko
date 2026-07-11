package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/saxocellphone/runko/platform/affected"
)

// Tool is one MCP tool definition as served by tools/list. Name,
// Description, and InputSchema are VERBATIM copies of the seven
// `"status": "v1"` entries in docs/spec/mcp-tools/catalog.json - the
// catalog is the contract (§8.3, §28.2 rule 2), this file is its
// transcription, and TestToolsMatchCatalog fails if the two ever drift.
// The input schemas' `$ref`s resolve against
// docs/spec/mcp-tools/common.schema.json.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// defaultPageSize is common.schema.json#/$defs/PageParams' default (§17.4's
// token-efficiency rule: compact lists by default).
const defaultPageSize = 50

// Tools are the seven read-only v1 tools (§8.3's MCP rescope, §17.4; the
// seventh, list_change_comments, graduated at stage 16 - §13.4.1): the
// other 18 catalog entries stay `"status": "deferred-v1.x"` and are
// deliberately NOT served.
var Tools = []Tool{
	{
		Name:        "list_projects",
		Description: "List projects, optionally filtered by name/path substring. Summary-shaped by default per the §8.2 context-budget rule.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{"query":{"type":"string","description":"Optional substring filter on name or path."},"page_size":{"$ref":"common.schema.json#/$defs/PageParams/properties/page_size"},"page_token":{"$ref":"common.schema.json#/$defs/PageParams/properties/page_token"}}}`),
	},
	{
		Name:        "get_project",
		Description: "Fetch the effective view of one project: manifest, capabilities, declared+inferred deps, effective owners.",
		InputSchema: json.RawMessage(`{"type":"object","required":["project"],"additionalProperties":false,"properties":{"project":{"type":"string","description":"Project id or name."},"detail":{"type":"string","enum":["summary","full"],"default":"summary"}}}`),
	},
	{
		Name:        "who_owns",
		Description: "Resolve effective owners for a path or a project (§7.3).",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"oneOf":[{"required":["path"]},{"required":["project"]}],"properties":{"path":{"type":"string"},"project":{"type":"string"}}}`),
	},
	{
		Name:        "get_affected",
		Description: "Compute affected projects for a set of paths, or re-fetch the computation already attached to a Change (§13.3, §14.4.3).",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"oneOf":[{"required":["paths"]},{"required":["change_id"]}],"properties":{"paths":{"type":"array","items":{"type":"string"}},"change_id":{"type":"string"}}}`),
	},
	{
		Name:        "search_code",
		Description: "Full-text code search via Zoekt (default engine, §8.2). Returns path hits tagged with project id, not a raw multi-GB grep dump.",
		InputSchema: json.RawMessage(`{"type":"object","required":["query"],"additionalProperties":false,"properties":{"query":{"type":"string"},"project":{"type":"string","description":"Optional: scope search to one project."},"page_size":{"$ref":"common.schema.json#/$defs/PageParams/properties/page_size"},"page_token":{"$ref":"common.schema.json#/$defs/PageParams/properties/page_token"}}}`),
	},
	{
		Name:        "get_merge_requirements",
		Description: "Owners + checks outstanding, in plain language, for both the Change page and agent callers (§8.3, §13.4, §13.5).",
		InputSchema: json.RawMessage(`{"type":"object","required":["change_id"],"additionalProperties":false,"properties":{"change_id":{"type":"string"}}}`),
	},
	{
		Name:        "list_change_comments",
		Description: "List review comments on a Change (§13.4.1): threads one level deep, anchored to the head_sha they were written against - a differing head means outdated, never repositioned.",
		InputSchema: json.RawMessage(`{"type":"object","required":["change_id"],"additionalProperties":false,"properties":{"change_id":{"type":"string"},"page_size":{"$ref":"common.schema.json#/$defs/PageParams/properties/page_size"},"page_token":{"$ref":"common.schema.json#/$defs/PageParams/properties/page_token"}}}`),
	},
}

// toolArgs is the decoded union of every v1 tool's arguments.
type toolArgs struct {
	Query     string   `json:"query"`
	Project   string   `json:"project"`
	Detail    string   `json:"detail"`
	Path      string   `json:"path"`
	Paths     []string `json:"paths"`
	ChangeID  string   `json:"change_id"`
	PageSize  int      `json:"page_size"`
	PageToken string   `json:"page_token"`
}

// CallTool dispatches one tool call and returns either the tool's
// schema-shaped output or an *Error (never both, never a bare error).
func (s *Server) CallTool(ctx context.Context, name string, rawArgs json.RawMessage) (interface{}, *Error) {
	var args toolArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, &Error{Code: "validation_failed", Message: fmt.Sprintf("arguments: %v", err)}
		}
	}
	switch name {
	case "list_projects":
		return s.listProjects(ctx, args)
	case "get_project":
		return s.getProject(ctx, args)
	case "who_owns":
		return s.whoOwns(ctx, args)
	case "get_affected":
		return s.getAffected(ctx, args)
	case "search_code":
		return s.searchCode(ctx, args)
	case "get_merge_requirements":
		return s.getMergeRequirements(ctx, args)
	case "list_change_comments":
		return s.listChangeComments(ctx, args)
	default:
		return nil, &Error{Code: "not_found", Field: "name", Message: fmt.Sprintf("no such tool %q", name)}
	}
}

// page decodes a page_token (this adapter's tokens are plain offsets - the
// daemon's list endpoints aren't paginated, so pagination is windowing
// done here) and clamps page_size to the schema's default when unset.
func page(args toolArgs) (offset, size int, err *Error) {
	size = args.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	if args.PageToken != "" {
		n, convErr := strconv.Atoi(args.PageToken)
		if convErr != nil || n < 0 {
			return 0, 0, &Error{Code: "validation_failed", Field: "page_token", Message: fmt.Sprintf("bad page_token %q", args.PageToken)}
		}
		offset = n
	}
	return offset, size, nil
}

// window slices [offset, offset+size) out of a list of length n, returning
// the bounds and the next page token ("" when the window reaches the end).
func window(n, offset, size int) (lo, hi int, next string) {
	if offset > n {
		offset = n
	}
	lo, hi = offset, offset+size
	if hi >= n {
		return lo, n, ""
	}
	return lo, hi, strconv.Itoa(hi)
}

func (s *Server) listProjects(ctx context.Context, args toolArgs) (interface{}, *Error) {
	offset, size, perr := page(args)
	if perr != nil {
		return nil, perr
	}
	indexed, err := s.Client.ListProjects(ctx)
	if err != nil {
		return nil, asError(err)
	}
	all := make([]ProjectSummary, 0, len(indexed))
	for _, p := range indexed {
		if args.Query != "" && !strings.Contains(p.Name, args.Query) && !strings.Contains(p.Path, args.Query) {
			continue
		}
		all = append(all, projectSummary(p))
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	lo, hi, next := window(len(all), offset, size)
	return ListProjectsOutput{Projects: all[lo:hi], NextPageToken: next}, nil
}

func (s *Server) getProject(ctx context.Context, args toolArgs) (interface{}, *Error) {
	if args.Project == "" {
		return nil, &Error{Code: "validation_failed", Field: "project", Message: "project is required"}
	}
	indexed, err := s.Client.ListProjects(ctx)
	if err != nil {
		return nil, asError(err)
	}
	p, ok := findProject(args.Project, indexed)
	if !ok {
		return nil, &Error{
			Code: "not_found", Field: "project",
			Message:    fmt.Sprintf("no project %q indexed at trunk", args.Project),
			Suggestion: "list_projects shows every known project",
		}
	}
	if args.Detail == "full" {
		return projectDetail(p), nil
	}
	return projectSummary(p), nil
}

func (s *Server) whoOwns(ctx context.Context, args toolArgs) (interface{}, *Error) {
	if (args.Path == "") == (args.Project == "") {
		return nil, &Error{Code: "validation_failed", Message: "exactly one of path or project is required"}
	}
	indexed, err := s.Client.ListProjects(ctx)
	if err != nil {
		return nil, asError(err)
	}
	if args.Project != "" {
		p, ok := findProject(args.Project, indexed)
		if !ok {
			return nil, &Error{Code: "not_found", Field: "project", Message: fmt.Sprintf("no project %q indexed at trunk", args.Project)}
		}
		return ownersResult(p), nil
	}
	p, ok := owningProject(args.Path, indexed)
	if !ok {
		return nil, &Error{
			Code: "not_found", Field: "path",
			Message:    fmt.Sprintf("no project owns %q", args.Path),
			Suggestion: "unowned paths fail closed in affected computation (§14.5.3); add a PROJECT.yaml above this path to own it",
		}
	}
	return ownersResult(p), nil
}

func (s *Server) getAffected(ctx context.Context, args toolArgs) (interface{}, *Error) {
	if (len(args.Paths) == 0) == (args.ChangeID == "") {
		return nil, &Error{Code: "validation_failed", Message: "exactly one of paths or change_id is required"}
	}
	var result affected.Result
	var err error
	if args.ChangeID != "" {
		result, err = s.Client.AffectedByChange(ctx, args.ChangeID)
	} else {
		result, err = s.Client.AffectedByPaths(ctx, args.Paths)
	}
	if err != nil {
		return nil, asError(err)
	}
	indexed, err := s.Client.ListProjects(ctx)
	if err != nil {
		return nil, asError(err)
	}
	return affectedComputation(result, indexed), nil
}

func (s *Server) searchCode(ctx context.Context, args toolArgs) (interface{}, *Error) {
	if args.Query == "" {
		return nil, &Error{Code: "validation_failed", Field: "query", Message: "query is required"}
	}
	offset, size, perr := page(args)
	if perr != nil {
		return nil, perr
	}
	// The daemon caps by hit count, not pages; ask for enough to fill the
	// requested window, over-fetching when a project filter will discard
	// hits from other projects (a v1 approximation - Zoekt itself has no
	// per-project index shards to scope to yet).
	num := offset + size
	if args.Project != "" {
		num *= 10
	}
	result, err := s.Client.Search(ctx, args.Query, num)
	if err != nil {
		return nil, asError(err)
	}
	hits := result.Hits
	if args.Project != "" {
		filtered := hits[:0:0]
		for _, h := range hits {
			if h.Project == args.Project {
				filtered = append(filtered, h)
			}
		}
		hits = filtered
	}
	lo, hi, next := window(len(hits), offset, size)
	out := searchOutput(result, hits[lo:hi])
	out.NextPageToken = next
	return out, nil
}

func (s *Server) getMergeRequirements(ctx context.Context, args toolArgs) (interface{}, *Error) {
	if args.ChangeID == "" {
		return nil, &Error{Code: "validation_failed", Field: "change_id", Message: "change_id is required"}
	}
	raw, err := s.Client.MergeRequirements(ctx, args.ChangeID)
	if err != nil {
		return nil, asError(err)
	}
	return raw, nil
}

// listChangeComments passes the daemon's GET .../comments response through
// verbatim (its {comments, next_page_token} shape IS the tool's
// output_schema - the get_merge_requirements stance). Pagination maps
// page_size/page_token onto the endpoint's own limit/offset; the seventh
// tool, graduated from the deferred catalog at stage 16 (§13.4.1, §17.4).
func (s *Server) listChangeComments(ctx context.Context, args toolArgs) (interface{}, *Error) {
	if args.ChangeID == "" {
		return nil, &Error{Code: "validation_failed", Field: "change_id", Message: "change_id is required"}
	}
	offset, size, perr := page(args)
	if perr != nil {
		return nil, perr
	}
	raw, err := s.Client.ListChangeComments(ctx, args.ChangeID, size, offset)
	if err != nil {
		return nil, asError(err)
	}
	return raw, nil
}

// asError normalizes a client error to *Error (client.go only produces
// *Error, but the type system doesn't know that).
func asError(err error) *Error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{Code: "backend_error", Message: err.Error(), Retryable: true}
}
