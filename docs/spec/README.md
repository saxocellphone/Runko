# Pre-session-1 spec artifacts

Three schema artifacts required before implementation sessions begin (design.md §26 #2/#3/#8, §28.4). All are JSON Schema (draft 2020-12); cross-file `$ref`s resolve relative to the referencing file.

| Artifact | File(s) | Spec section |
|---|---|---|
| `PROJECT.yaml` v1 schema | `project.schema.json` | §10.1-10.4, §7.2 |
| MCP tool catalog | `mcp-tools/catalog.json` (25 tools) + `mcp-tools/common.schema.json` (shared types) | §8.2, §8.3 |
| Webhook + CheckRun schemas | `webhooks/webhook-envelope.schema.json`, `webhooks/checkrun.schema.json` | §14.4.1, §14.4.2 |

These are the single source of truth for generated types (§28.2 item 2): platform API, `runko-ci`, and the MCP server all generate from these files. Do not hand-duplicate these shapes in package code — regenerate instead.
