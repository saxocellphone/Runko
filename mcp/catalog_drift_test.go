package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// catalogTool is the slice of docs/spec/mcp-tools/catalog.json this test
// cares about.
type catalogTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Status       string          `json:"status"`
	ReadOnly     bool            `json:"read_only"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
}

func loadCatalog(t *testing.T) []catalogTool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "docs", "spec", "mcp-tools", "catalog.json"))
	if err != nil {
		t.Fatalf("read catalog.json: %v", err)
	}
	var catalog struct {
		Tools []catalogTool `json:"tools"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		t.Fatalf("unmarshal catalog.json: %v", err)
	}
	if len(catalog.Tools) == 0 {
		t.Fatalf("catalog.json has no tools - wrong file?")
	}
	return catalog.Tools
}

func normalizeJSON(t *testing.T, raw json.RawMessage) interface{} {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return v
}

// TestToolsMatchCatalog pins this package's Tools table to the catalog's
// six `"status": "v1"` entries: same name set (no more, no fewer - serving
// a deferred tool would silently widen the v1 surface §8.3 deliberately
// shrank), same descriptions, and byte-for-byte-equivalent input schemas.
// The catalog is the contract; this package is its transcription, and this
// test is what keeps "transcription" honest.
func TestToolsMatchCatalog(t *testing.T) {
	v1 := map[string]catalogTool{}
	for _, ct := range loadCatalog(t) {
		if ct.Status == "v1" {
			if !ct.ReadOnly {
				t.Fatalf("catalog tool %s is v1 but not read_only - the v1 adapter is read-only by scope (§17.4)", ct.Name)
			}
			v1[ct.Name] = ct
		}
	}
	if len(v1) != len(Tools) {
		t.Fatalf("catalog has %d v1 tools, this package serves %d", len(v1), len(Tools))
	}
	for _, tool := range Tools {
		ct, ok := v1[tool.Name]
		if !ok {
			t.Fatalf("served tool %q is not a v1 tool in the catalog", tool.Name)
		}
		if tool.Description != ct.Description {
			t.Errorf("%s: description drifted:\n  catalog: %q\n  served:  %q", tool.Name, ct.Description, tool.Description)
		}
		if !reflect.DeepEqual(normalizeJSON(t, tool.InputSchema), normalizeJSON(t, ct.InputSchema)) {
			t.Errorf("%s: input schema drifted from catalog.json:\n  catalog: %s\n  served:  %s", tool.Name, ct.InputSchema, tool.InputSchema)
		}
	}
}
