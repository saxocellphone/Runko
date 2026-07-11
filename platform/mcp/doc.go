// Package mcp implements the MCP thin remote adapter (docs/design.md §8.3,
// §17.4, §28.3 stage 12): a newline-delimited JSON-RPC 2.0 stdio server
// exposing exactly the seven read-only `"status": "v1"` tools from
// docs/spec/mcp-tools/catalog.json (list_projects, get_project, who_owns,
// get_affected, search_code, get_merge_requirements) as thin wrappers over
// runkod's REST API - the same handlers the CLI and every other client use.
// The CLI is the primary agent interface; this adapter exists for clients
// that can't shell out, not as a second backend (§8.3, decided 2026-07-07).
// The other 19 catalog tools are `"status": "deferred-v1.x"` and
// deliberately not served.
//
// Tool definitions are transcriptions of the catalog, pinned by a drift
// test (TestToolsMatchCatalog); outputs are contract-tested against
// docs/spec/mcp-tools/common.schema.json. Every tool-call failure uses the
// shape at common.schema.json#/$defs/Error - never a bare string for a
// machine caller (§6.5, §8.3). Zero dependencies beyond stdlib: the MCP
// stdio transport is line-framed JSON-RPC, which does not need an SDK
// (the same lean-dependency stance that kept Zoekt a process, §28.2).
package mcp
