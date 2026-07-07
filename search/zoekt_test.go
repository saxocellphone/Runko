package search

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/saxocellphone/runko/internal/clierr"
)

func TestZoektSearcherParsesHits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("q"); got != "checkout" {
			t.Fatalf("expected q=checkout, got %q", got)
		}
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Fatalf("expected format=json, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"result": {
				"FileMatches": [
					{
						"FileName": "commerce/checkout/main.go",
						"Repo": "monorepo",
						"Matches": [
							{
								"LineNum": 12,
								"Before": "func setup() {",
								"After": "}",
								"Fragments": [
									{"Pre": "func ", "Match": "Checkout", "Post": "() error {"}
								]
							}
						]
					}
				]
			}
		}`))
	}))
	defer server.Close()

	searcher := ZoektSearcher{BaseURL: server.URL}
	result, err := searcher.Search(context.Background(), "checkout", SearchOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if result.Query != "checkout" {
		t.Fatalf("expected query echoed back, got %+v", result)
	}
	if len(result.Hits) != 1 {
		t.Fatalf("expected 1 hit, got %+v", result.Hits)
	}
	hit := result.Hits[0]
	if hit.Path != "commerce/checkout/main.go" || hit.LineNumber != 12 {
		t.Fatalf("unexpected path/line: %+v", hit)
	}
	if hit.Line != "func Checkout() error {" {
		t.Fatalf("expected fragments reassembled into the full line, got %q", hit.Line)
	}
	if hit.Before != "func setup() {" || hit.After != "}" {
		t.Fatalf("expected context lines to pass through, got %+v", hit)
	}
}

func TestZoektSearcherMultipleFragmentsPerLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"result": {
				"FileMatches": [
					{
						"FileName": "a.go",
						"Matches": [
							{
								"LineNum": 1,
								"Fragments": [
									{"Pre": "x := ", "Match": "foo", "Post": ""},
									{"Pre": "(", "Match": "foo", "Post": ")"}
								]
							}
						]
					}
				]
			}
		}`))
	}))
	defer server.Close()

	searcher := ZoektSearcher{BaseURL: server.URL}
	result, err := searcher.Search(context.Background(), "foo", SearchOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := result.Hits[0].Line; got != "x := foo(foo)" {
		t.Fatalf("expected reassembled multi-fragment line, got %q", got)
	}
}

func TestZoektSearcherEmptyResultIsNoHits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	searcher := ZoektSearcher{BaseURL: server.URL}
	result, err := searcher.Search(context.Background(), "nothing", SearchOptions{})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Hits) != 0 {
		t.Fatalf("expected no hits, got %+v", result.Hits)
	}
}

func TestZoektSearcherServerErrorIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer server.Close()

	searcher := ZoektSearcher{BaseURL: server.URL}
	_, err := searcher.Search(context.Background(), "q", SearchOptions{})
	if err == nil {
		t.Fatalf("expected an error on a 500 response")
	}
}

func TestZoektSearcherUnreachableIsError(t *testing.T) {
	searcher := ZoektSearcher{BaseURL: "http://127.0.0.1:1"}
	_, err := searcher.Search(context.Background(), "q", SearchOptions{})
	if err == nil {
		t.Fatalf("expected an error contacting an unreachable server")
	}
}

// TestZoektSearcherEmptyBaseURLIsNotConfigured guards the "NO silent
// git-grep fallback" rule (§8.2, doc.go): a zero-value ZoektSearcher (e.g.
// constructed but never given a --search-url) must behave exactly like
// NotConfiguredSearcher, not silently return an empty/successful result
// that a caller could mistake for "there are no matches".
func TestZoektSearcherEmptyBaseURLIsNotConfigured(t *testing.T) {
	searcher := ZoektSearcher{}
	_, err := searcher.Search(context.Background(), "q", SearchOptions{})
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Code != "search_not_configured" {
		t.Fatalf("expected code search_not_configured, got %+v", ce)
	}
}

func TestNotConfiguredSearcherReturnsStructuredError(t *testing.T) {
	_, err := NotConfiguredSearcher{}.Search(context.Background(), "q", SearchOptions{})
	var ce *clierr.Error
	if !errors.As(err, &ce) {
		t.Fatalf("expected a *clierr.Error, got %T: %v", err, err)
	}
	if ce.Code != "search_not_configured" {
		t.Fatalf("expected code search_not_configured, got %+v", ce)
	}
}
