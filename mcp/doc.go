// Package mcp implements the MCP server (docs/design.md §8.3, §9.1), generated
// from the tool catalog at docs/spec/mcp-tools/catalog.json rather than
// hand-written per tool (§28.2 rule 2). Every tool call error uses the shape at
// docs/spec/mcp-tools/common.schema.json#/$defs/Error - never a bare string or
// an HTML error page for a machine caller (§6.5, §8.3).
package mcp
