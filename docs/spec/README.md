# Pre-session-1 (and later) spec artifacts

Schema artifacts required before their dependent implementation sessions begin (design.md §26, §28.4). All are JSON Schema (draft 2020-12); cross-file `$ref`s resolve relative to the referencing file.

| Artifact | File(s) | Spec section | Blocks |
|---|---|---|---|
| `PROJECT.yaml` v1 schema | `project.schema.json` | §10.1-10.4, §7.2 | stages 3-5 |
| MCP tool catalog | `mcp-tools/catalog.json` (25 tools) + `mcp-tools/common.schema.json` (shared types) | §8.2, §8.3 | stage 11 |
| Webhook + CheckRun schemas | `webhooks/webhook-envelope.schema.json`, `webhooks/checkrun.schema.json` | §14.4.1, §14.4.2 | stages 8-9 |
| Build-graph adapter contract (§26 #13) | `build-adapter/README.md` (engine interface, Bazel/Buck2 query recipes) + `build-adapter/refinement.schema.json` | §14.5.4 | stages 9b-9c |

These are the single source of truth for generated types (§28.2 item 2): platform API, `runko-ci`, and the MCP server all generate from these files. Do not hand-duplicate these shapes in package code — regenerate instead.
