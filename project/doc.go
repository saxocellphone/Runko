// Package project implements the intent -> files pipeline (docs/design.md
// §10.1): CreateProjectIntent -> ValidateIntent -> ResolveTemplate -> Plan (file
// list + effective PROJECT.yaml + owners) -> Apply (commit or workspace overlay)
// + index control plane. Also add_capability and adopt_path_as_project (§8.5,
// §10.4). The manifest schema is docs/spec/project.schema.json and the intent
// shape is docs/spec/mcp-tools/common.schema.json#/$defs/CreateProjectIntent -
// generate types from those rather than redefining them here.
//
// L0 (name/type/owners) must never require L2/L3 fields to create successfully
// (§6.2 hard rule).
//
// Intent and Manifest below are hand-written mirrors of the JSON Schemas in
// docs/spec/ (no JSON-Schema-to-Go codegen pipeline exists yet - see §28.2
// rule 2). Keep them in sync by hand until that pipeline lands.
package project
