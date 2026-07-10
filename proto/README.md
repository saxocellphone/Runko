# Runko Connect/gRPC API

The wire contract between the web frontend (`web/`) and `runkod`
(docs/design.md §17.4 — decided 2026-07-07 on both halves: Connect-ES on
the client, connect-go on the server, no Envoy/grpc-web proxy). Field
numbers are wire-frozen; `buf lint` runs clean against this directory.

## How it's served and consumed

- **Server**: `runkod/rpc.go` mounts all services on the daemon's existing
  `net/http` mux, behind the same auth as `/api/*` (bearer token or HTTP
  Basic against the principal registry), with permissive-origin CORS
  (auth is header-borne, never cookies). Every RPC wraps the same decision
  cores the REST handlers use (`runkod/actions.go`), so the two transports
  cannot disagree on gate semantics; errors carry `runko.v1.ErrorDetail`
  as a typed Connect error detail with the same stable `code` strings the
  CLI (`internal/clierr`) and MCP use (§6.5).
- **Generated code is committed**: Go stubs at `proto/gen/runko/v1`
  (regenerate with `go run github.com/bufbuild/buf/cmd/buf generate` —
  local `protoc-gen-go`/`protoc-gen-connect-go`, see `buf.gen.yaml`'s
  header) and TypeScript at `web/src/gen` (`cd web && npm run gen`).
  Never hand-edit either; only proto edits require buf.
- **Other clients stay on REST**: the `runko`/`runko-ci` CLIs and
  `runko mcp serve` use `runkod/api.go` unchanged. This surface exists for
  the web UI; whether REST and Connect ever collapse into one is open.

## Design choices (settled)

1. **Message shapes mirror `docs/spec/mcp-tools/common.schema.json`
   field-for-field** wherever a concept has a schema there — one shape per
   concept across CLI, MCP, and web (the `docs/cli-contract.md`
   single-contract rule).
2. **Connect over raw grpc-go + grpc-web + Envoy**: browsers can't speak
   raw gRPC, and Connect serves gRPC, gRPC-Web, and plain JSON/HTTP from
   one `.proto` set directly on `net/http` — no sidecar process, matching
   this repo's processes-over-heavy-deps posture.
3. **`GetProject` always returns `ProjectDetail`** (superset of the
   summary) — a single static response type instead of MCP's runtime
   `oneOf`.
4. **Errors**: transport-level Connect codes (`NOT_FOUND`,
   `INVALID_ARGUMENT`, `FAILED_PRECONDITION`) + `ErrorDetail.code` for the
   stable string every client branches on.
5. **Auth**: `authorization` header (bearer or Basic); the signed-in
   principal drives approve/land attribution server-side.
6. **Pagination**: plain `page_size`/`page_token` per request message.

## Files

| File | Covers |
|---|---|
| `common.proto` | Shared messages/enums, mirroring `common.schema.json` $defs |
| `projects.proto` | `ProjectService`: list/get/who-owns; preview-create/create (creation opens an ordinary Change, §6.9/§10.1) |
| `changes.proto` | `ChangeService`: get/list/stack/diff/affected/merge-requirements/approve/land/abandon/rerun |
| `workspaces.proto` | `WorkspaceService`: create/list/get/update-base |
| `search.proto` | `SearchService`: code search |
| `repo.proto` | `RepoService`: tree/blob browsing, path-scoped history (`ListCommits`), blame (`BlameFile`) |

Out of scope, deliberately: CI-facing RPCs (`runko-ci` is a CI runner, it
stays on REST), workspace *snapshotting* (that's local git + a push
through the receive funnel, not a control-plane call), and write-tool
parity with MCP's deferred-v1.x catalog — RPCs are added here as those
graduate, not pre-built (anti-Boq, §6.2).

## Still open

Write-tool RPCs as the deferred MCP tools graduate; server-side pagination
when a real list outgrows one response.
