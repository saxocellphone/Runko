package mcp

// A tool description is what an agent reads before deciding to call the
// tool - product copy, not an engineering record. Spec citations ("§8.2",
// "docs/design.md") resolve to nothing for the caller, and design.md is a
// retired historical document. The catalog is checked alongside the served
// list, since TestToolsMatchCatalog binds the two.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var forbiddenInToolCopy = []string{"§", "design.md"}

func TestToolDescriptionsCarryNoSpecCitations(t *testing.T) {
	for _, tool := range Tools {
		for _, bad := range forbiddenInToolCopy {
			if strings.Contains(tool.Description, bad) {
				t.Errorf("tool %s: description cites %q - it is read by callers, not contributors:\n  %s",
					tool.Name, bad, tool.Description)
			}
		}
	}
}

// The catalog's tool entries are the same copy one release earlier: a
// deferred tool's description becomes a served description the day it
// ships, so citations are caught here rather than at promotion time. The
// file's own top-level "description" is metadata ABOUT the artifact and
// is deliberately not checked.
func TestCatalogToolDescriptionsCarryNoSpecCitations(t *testing.T) {
	for _, ct := range loadCatalog(t) {
		for _, bad := range forbiddenInToolCopy {
			if strings.Contains(ct.Description, bad) {
				t.Errorf("catalog tool %s: description cites %q:\n  %s", ct.Name, bad, ct.Description)
			}
		}
	}
	// Input/output schemas are served verbatim too - their property
	// descriptions reach the same reader.
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "spec", "mcp-tools", "catalog.json"))
	if err != nil {
		t.Fatalf("read catalog.json: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		// Six spaces of indent or more is inside a tool entry; the
		// artifact's own metadata sits at two.
		if !strings.HasPrefix(line, "      ") || !strings.Contains(line, `"description"`) {
			continue
		}
		for _, bad := range forbiddenInToolCopy {
			if strings.Contains(line, bad) {
				t.Errorf("catalog.json: served copy cites %q:\n  %s", bad, strings.TrimSpace(line))
			}
		}
	}
}
