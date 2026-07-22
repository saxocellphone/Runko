package search

import (
	"context"

	"github.com/saxocellphone/runko/internal/clierr"
)

// CodeSearcher is the search_code seam (§8.3): find code matching a query
// across the monorepo's trunk. Implementations run out-of-process (see
// doc.go) - this package never does its own text scanning.
type CodeSearcher interface {
	Search(ctx context.Context, query string, opts SearchOptions) (*Result, error)
}

// SearchOptions configures one Search call. The zero value is a reasonable
// default (an implementation-chosen result cap).
type SearchOptions struct {
	// Num caps the number of hits returned. 0 means "use the searcher's own
	// default", not "no limit".
	Num int
}

// Hit is one matching line, before project-tagging (runkod's REST handler
// fills in Project via the same longest-prefix rule affected.Compute uses -
// this package has no dependency on index/affected, to stay a leaf package
// like receive/buildadapter).
type Hit struct {
	Path       string
	Project    string
	LineNumber int
	Line       string
	Before     string
	After      string
}

// Result is one Search call's full response.
type Result struct {
	Query string
	Hits  []Hit
}

// NotConfiguredSearcher is the default CodeSearcher when no zoekt-webserver
// URL is configured (cmd/runkod's --search-url). It always returns a
// structured §6.5 error - deliberately not a stub scanner that silently
// does nothing, and deliberately NOT a git-grep fallback (§8.2): a caller
// asking to search code needs to know indexing isn't wired up, not receive
// an unranked, unindexed substitute it might mistake for the real thing.
type NotConfiguredSearcher struct{}

func (NotConfiguredSearcher) Search(_ context.Context, _ string, _ SearchOptions) (*Result, error) {
	return nil, &clierr.Error{
		Code:       "search_not_configured",
		Field:      "search",
		Message:    "code search is not configured on this runkod instance",
		Suggestion: "start runkod with --search-url pointing at a zoekt-webserver, and ensure zoekt-git-index has run at least once",
	}
}

var (
	_ CodeSearcher = NotConfiguredSearcher{}
	_ CodeSearcher = (*ZoektSearcher)(nil)
)
