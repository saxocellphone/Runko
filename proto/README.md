# Runko gRPC API (draft)

This is a **draft schema** for the web frontend (§28.3 stage 13, not yet
started) to talk to `runkod`, per the user's 2026-07-07 direction that
frontend/backend communication should use gRPC. It exists so a frontend
agent can start building UI against a stable-looking contract in parallel
with the Go server-side work, not because the architecture below is fully
decided - see "Open questions" before treating any of this as final.

## Status

- **Consumed by the web frontend since 2026-07-07** (`web/`, stage 13's
  frontend half): TypeScript types are generated into `web/src/gen`
  (committed; `cd web && npm run gen` regenerates via `@bufbuild/buf` +
  local `protoc-gen-es`), and `buf lint` runs clean against this
  directory. Field numbers are therefore wire-frozen now, as
  `common.proto`'s header warns.
- **Served since 2026-07-07** (stage 13's server half): runkod mounts
  connect-go handlers for all six services on its existing `net/http` mux
  (`runkod/rpc.go`; Go stubs committed at `gen/runko/v1`, regenerate with
  `buf generate` per `buf.gen.yaml`'s header). Every RPC wraps the same
  decision cores the REST handlers use (`runkod/actions.go`), and errors
  carry `ErrorDetail` as a Connect error detail per item 4 below. The web
  UI talks to it via `VITE_RUNKO_URL`/`VITE_RUNKO_TOKEN`; the fake
  transport remains mounted at `/demo` (see web/README.md).
- `GetChangeStack` + `GetChangeDiff` (and the FileDiff/DiffHunk/DiffLine
  shapes) were added to `changes.proto` for the stacked-diff view - the
  original draft had no way to render a diff at all. Their REST
  equivalents don't exist yet either; the fake transport documents the
  intended semantics.
- **Recorded in `docs/design.md`** (§17.4 + the 2026-07-07 changelog rows)
  as a committed architecture decision: Connect on both halves.
- Existing clients (`runko`/`runko-ci` CLI, `runko mcp serve`) keep using
  `runkod`'s REST API (`runkod/api.go`) unchanged. This draft does not
  propose replacing that surface - only adding the web frontend's contract
  as gRPC. Whether `runkod`'s REST handlers eventually become thin
  wrappers over the same gRPC service handlers (or vice versa) is an open
  question, not assumed here.

## Design choices made in this draft (need confirming, not yet decided)

1. **Message shapes mirror `docs/spec/mcp-tools/common.schema.json`
   field-for-field** wherever a concept already has a schema there
   (`ProjectSummary`, `ProjectDetail`, `OwnersResult`,
   `AffectedComputation`, `ChangeSummary`, `MergeRequirements`,
   `WorkspaceSummary`). Rationale: one shape per concept across CLI, MCP,
   and web, not three independently-drifting ones - the same reasoning
   `docs/cli-contract.md`'s "single-contract rule" already states for
   CLI/MCP.
2. **Recommend [Connect](https://connectrpc.com/) over raw grpc-go +
   grpc-web + Envoy.** Browsers cannot speak raw gRPC (HTTP/2 trailers
   aren't accessible to `fetch`/`XMLHttpRequest`), so *some* adaptation
   layer is mandatory - the usual answer is a separate grpc-web proxy
   (Envoy). Connect instead generates a server that speaks gRPC, gRPC-Web,
   *and* plain JSON/HTTP from the same `.proto` set, mounted directly on
   Go's `net/http`, no extra process. That fits this repo's established
   posture of shelling out to real binaries instead of adding heavyweight
   library/infra dependencies (Zoekt, Bazel, gitleaks are all processes,
   not `go.mod` imports) - Envoy would be the odd one out. `buf.gen.yaml`
   drafts `connectrpc/go` + `connectrpc/es` codegen on this assumption;
   swap it out if Connect is rejected.
3. **`GetProject` always returns `ProjectDetail`** (a strict superset of
   `ProjectSummary`), unlike the MCP tool's `oneOf(summary, detail)` -
   JSON-RPC can return either shape at runtime, a single gRPC RPC has one
   static response type, so returning the superset and letting the
   frontend pick fields is the idiomatic proto equivalent.
4. **Errors as `google.rpc.Status` + an `ErrorDetail` message
   (`common.proto`)**, not an inline "error" field on every response.
   Transport-level codes (`NOT_FOUND`, `INVALID_ARGUMENT`,
   `FAILED_PRECONDITION` for policy violations) stay meaningful to generic
   gRPC tooling and browser devtools; `ErrorDetail.code` carries the same
   stable string every other client (`internal/clierr.Error`, `mcp.Error`)
   already branches on. Not yet wired to any actual `google.rpc.Status`
   usage since there's no server implementation yet - flagged here so the
   frontend agent builds error handling against this shape from the start
   rather than improvising one.
5. **Auth**: `authorization: Bearer <token>` request metadata, mirroring
   the REST API's current scheme, until the named-token principal
   registry (§28.3 stage 12c, in progress) and eventually OIDC (§15.1)
   land. `ApproveChangeRequest.approved_by` is still client-asserted text
   for the same reason `runko change approve --by` is today - expected to
   move to request-metadata-derived identity once principals exist.
6. **Pagination**: plain `page_size`/`page_token` fields per request
   message, not a shared `PageParams` message - idiomatic per-RPC proto
   style, same information as `common.schema.json#/$defs/PageParams`.

## Deliberately out of scope for this draft

- `ReportCheck` / anything CI-facing - `runko-ci` stays on the REST API
  (it's a CI runner, not the web frontend; no reason to add gRPC there).
- Workspace *snapshotting* - `runko workspace snapshot` is local git plus
  a push through the ordinary receive funnel, not a control-plane call;
  it has no RPC in `workspaces.proto` for the same reason `git push`
  itself doesn't.
- Write-tool parity with MCP's deferred-v1.x catalog (`create_project`,
  `create_change`, etc.) - add RPCs here as those graduate, don't
  pre-build a surface nothing calls yet (anti-Boq, §6.2's spirit).
- Restricted-visibility project filtering (§15.2) - the read-ACL model
  isn't implemented anywhere yet (REST included); this draft doesn't
  invent gRPC-specific enforcement ahead of that.

## Files

| File | Covers |
|---|---|
| `common.proto` | Shared messages/enums, mirroring `common.schema.json` $defs |
| `projects.proto` | `ProjectService`: list/get/who-owns |
| `changes.proto` | `ChangeService`: get/list/stack/diff/affected/merge-requirements/approve/land/abandon/rerun |
| `workspaces.proto` | `WorkspaceService`: create/list/get/update-base |
| `search.proto` | `SearchService`: code search |
| `repo.proto` | `RepoService`: read-only tree/blob browsing (stage 13 explorer; REST equivalents pending, like the stack/diff RPCs) |

`ListChanges`, `AbandonChange`, and `RerunCheck` correspond to REST
endpoints being added in parallel (§28.3 stage 12c, slice 3) - this draft
assumes they exist so the frontend can be built against the intended full
surface rather than today's partial one.

## Next steps (for whoever picks this up)

All four original items closed 2026-07-07 with stage 13's server half:

1. ~~Confirm or reject the Connect recommendation~~ **Confirmed on both
   halves**: `web/` on Connect-ES, runkod on connect-go (`runkod/rpc.go`).
2. ~~Install buf, run buf lint / buf generate~~ Done for TS
   (`web/src/gen`) and Go (`gen/runko/v1`, local plugins - see
   `buf.gen.yaml`'s header for the regenerate incantation).
3. ~~Record the confirmed decision in `docs/design.md`~~ Done (§17.4 +
   changelog).
4. ~~Implement `GetChangeStack`/`GetChangeDiff` server-side~~ Done
   (`runkod/rpc.go`, `runkod/diff.go`), plus `RepoService` tree/blob
   (`runkod/repo.go`).

Still open: write-tool RPCs as the MCP catalog's deferred-v1.x tools
graduate, server-side pagination when a real list outgrows one response,
and request-metadata-derived identity for `approved_by` once real AuthN
(§15.1) lands.
