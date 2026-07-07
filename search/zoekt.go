package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ZoektSearcher implements CodeSearcher via a stdlib HTTP client against a
// real zoekt-webserver's JSON search API (its /search route with
// ?format=json - see cmd/zoekt-webserver and web/server.go in the zoekt
// module, read directly from source since it is not a go.mod dependency
// here, per doc.go). No client library is vendored.
type ZoektSearcher struct {
	// BaseURL is the zoekt-webserver's address, e.g. "http://127.0.0.1:6070".
	// Empty means "not configured" - Search returns the same structured
	// error NotConfiguredSearcher does, so a ZoektSearcher zero value is
	// always safe to call.
	BaseURL string
	Client  *http.Client
}

// The subset of zoekt-webserver's JSON response shape (web.ApiSearchResult
// -> ResultInput -> FileMatch -> Match -> Fragment) this client needs.
// Field names/JSON keys match the real structs exactly (verified by reading
// web/api.go in the cached module source) - untagged Go fields there
// serialize under their literal (capitalized) field name.
type apiSearchResult struct {
	Result *apiResultInput `json:"result"`
}

type apiResultInput struct {
	FileMatches []apiFileMatch `json:"FileMatches"`
}

type apiFileMatch struct {
	FileName string     `json:"FileName"`
	Repo     string     `json:"Repo"`
	Matches  []apiMatch `json:"Matches"`
}

type apiMatch struct {
	LineNum   int           `json:"LineNum"`
	Fragments []apiFragment `json:"Fragments"`
	Before    string        `json:"Before"`
	After     string        `json:"After"`
}

type apiFragment struct {
	Pre   string `json:"Pre"`
	Match string `json:"Match"`
	Post  string `json:"Post"`
}

// Search calls GET <BaseURL>/search?q=<query>&format=json&num=<n>. Per
// web/snippets.go's formatResults (the code that builds Fragments server
// side): every fragment but the last has an empty Post, and only the last
// carries the line's tail - so the original line reconstructs as each
// fragment's Pre+Match concatenated, in order, followed by the final
// fragment's Post.
func (z ZoektSearcher) Search(ctx context.Context, query string, opts SearchOptions) (*Result, error) {
	base := strings.TrimSuffix(z.BaseURL, "/")
	if base == "" {
		return NotConfiguredSearcher{}.Search(ctx, query, opts)
	}

	num := opts.Num
	if num <= 0 {
		num = 50
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	q.Set("num", strconv.Itoa(num))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("zoekt search: build request: %w", err)
	}

	client := z.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zoekt search: contact %s: %w", base, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("zoekt search: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("zoekt search: %s returned %d: %s", base, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed apiSearchResult
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("zoekt search: parse response: %w", err)
	}

	result := &Result{Query: query}
	if parsed.Result == nil {
		return result, nil
	}
	for _, fm := range parsed.Result.FileMatches {
		for _, m := range fm.Matches {
			result.Hits = append(result.Hits, Hit{
				Path:       fm.FileName,
				LineNumber: m.LineNum,
				Line:       lineFromFragments(m.Fragments),
				Before:     m.Before,
				After:      m.After,
			})
		}
	}
	return result, nil
}

func lineFromFragments(frags []apiFragment) string {
	var b strings.Builder
	for _, f := range frags {
		b.WriteString(f.Pre)
		b.WriteString(f.Match)
	}
	if len(frags) > 0 {
		b.WriteString(frags[len(frags)-1].Post)
	}
	return b.String()
}

var _ CodeSearcher = ZoektSearcher{}
