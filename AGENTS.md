# Agent instructions for this repo

Runko is a monorepo platform, currently being implemented from `docs/design.md` (the full design spec). This file is the short version; go read the cited section before editing anything non-trivial.

## Commands

```bash
export PATH="$HOME/.local/go/bin:$PATH"   # go is not on the default PATH in this environment
make check       # fmt + vet + test, all packages, target < 30s
go test ./...
go build ./...
```

No compose stack, CI, or web UI exist yet — don't assume they're runnable.

## Layout map

```
docs/design.md    # full design spec — cite §s in commits/comments, don't re-derive decisions
docs/spec/        # generated-type source of truth: PROJECT.yaml schema, MCP tool catalog, webhook/CheckRun schemas
receive/          # magic-ref + Change-Id + policy + secret scan
land/             # rebase-land + optimistic revalidation
affected/         # pure function: paths/deps -> affected projects
checks/           # Checks API, merge requirements, check-set policies
project/          # intent -> files pipeline, templates, preview
mcp/              # MCP server, generated from docs/spec/mcp-tools/catalog.json
core/             # shared interfaces (MonorepoStore, etc.)
cmd/runko/        # human/agent CLI
cmd/runko-ci/     # CI-facing CLI
```

## Rules

- **Read the design.md section a package cites before changing that package.** The spec is decided, not a discussion draft — implementing it is transcription, not a fresh design exercise. If you think a decision is wrong, say so; don't silently diverge.
- **One package per session/PR.** Don't edit packages two hops away from your focus (design.md §28.2 rule 7).
- **Never hand-edit generated code** (sqlc, oapi-codegen, or anything generated from `docs/spec/*.json`). Regenerate instead.
- **Shell out to system `git`.** Never use a Git-in-Go library — the spec requires matching real upstream Git behavior exactly.
- **No mocking git in tests.** Use the git-fixture harness (throwaway repos from short scripts + golden-file diffs) instead.
- **Structured errors everywhere**, matching `docs/spec/mcp-tools/common.schema.json#/$defs/Error`: `{ code, message, retryable, field?, suggestion?, doc_url? }`. Every tool/API/CLI failure uses this shape — never a bare string or an HTML error page for a machine caller (design.md §6.5, §8.3).
- **No UI polish, no dependency additions mid-session, no refactors outside the current package** until the end-to-end loop (`compose up → create project → change → land`) is green (design.md §28.5).
