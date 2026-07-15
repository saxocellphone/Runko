# runkod's contract surface (§13.3.1)

Every API runkod serves lives here, in the serving project's own
boundary — the standalone `proto/` project this replaced (migrated
2026-07-15) made contracts a *place* instead of a property of their
owner. One buf module (`buf.yaml`), one codegen home (`buf.gen.yaml`),
generated Go committed at `gen/` (the `internal/dbgen` convention).

- **`runko/v1`** — the web ↔ runkod Connect surface (design.md §17.4):
  `ProjectService`/`ChangeService`/`WorkspaceService`/`SearchService`/
  `RepoService`, served by `runkod/rpc.go` behind the same auth as
  `/api/*`. Message shapes mirror `docs/spec/mcp-tools/common.schema.json`
  field-for-field (the single-contract rule); field numbers are
  wire-frozen. TypeScript for the web UI generates from THIS directory
  (`cd web && npm run gen`) into `web/src/gen` — module-root-relative
  descriptor names are unchanged from the old layout, so the migration
  caused zero TS import churn.
- **`mailer/v1`** — the invite-request drain feed (`InviteFeedService`),
  operator-gated, consumed by `runko-mailer` through its declared
  `consumes: [runkod]` edge.

Regenerate after a `.proto` edit, with the plugins on PATH (~/go/bin):

```
go install google.golang.org/protobuf/cmd/protoc-gen-go
go install connectrpc.com/connect/cmd/protoc-gen-connect-go
(cd runkod/proto && go run github.com/bufbuild/buf/cmd/buf@latest generate)
```

Never hand-edit `gen/`. Consumers of these contracts declare a
`consumes:` (or build-grade `dependencies:`) edge on runkod — receive
enforces it (§13.3.1), and the consumes closure re-tests clients exactly
when files under this directory change.
