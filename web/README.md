# Runko web frontend

The stage-13 web UI (docs/design.md §17.2, §28.3), built against the
draft gRPC contract in `proto/runko/v1/` via
[Connect-ES](https://connectrpc.com/) — confirming proto/README.md's
Connect recommendation on the client side. Design language is inspired by
[Graphite](https://graphite.dev): stacked changes rendered as a rail with
per-change status dots, trunk at the bottom, quiet neutral surfaces with a
single violet accent, light + dark themes.

## What exists

- **Changes inbox** — open changes grouped into stacks (client-side mirror
  of `GetChangeStack`'s derived relation, see `src/lib/stacks.ts`), with
  review/checks/mergeable chips per change and an agent badge for
  agent-authored changes (§8.7). Landed/abandoned tabs.
- **Change page** — the stacked diff view: per-change scoped diff
  (`GetChangeDiff` is `base..head`, so a stacked change shows only its own
  delta), stack panel, §13.5 merge gates (owners + checks) with
  approve/rerun, land/abandon actions gated on the same `mergeable` bool
  the server reports.
- **Browse** — barebones repo explorer (`RepoService`: lazy directory
  tree + file viewer, deep-linkable as `/browse/<path>`, project-badged
  via longest-prefix ownership).
- **Projects / project detail / workspaces / code search** — thinner reads
  over the corresponding services.

## Transport: real vs. demo

`src/api/client.ts` picks the transport at startup:

- `VITE_RUNKO_URL=http://host:port npm run dev` — Connect protocol against
  a runkod serving connect-go handlers. **No such server exists yet** (the
  proto is a draft; server-side wiring is the other half of stage 13).
- unset — an in-memory fake transport (`src/api/fake/`) serving a coherent
  demo scene through the same generated types, with real mutation
  semantics (approve/land/rerun/abandon) so every flow is exercisable.
  The sidebar shows a "Demo data" badge in this mode.

## Commands

```bash
npm install
npm run dev        # vite dev server (demo data unless VITE_RUNKO_URL set)
npm run check      # tsc + oxlint + vitest + production build (CI runs this)
npm run test       # vitest only
npm run gen        # regenerate src/gen from ../proto (buf + protoc-gen-es)
npm run screenshot # headless visual smoke: screenshots into screenshots/
                   # (needs: npx playwright-core install chromium-headless-shell,
                   #  and a dev server on :5173 or BASE_URL)
```

`src/gen/` is committed (the `internal/dbgen` convention): only proto
edits require buf, consumers and CI never do.

## Conventions

- One shape per concept: everything on the wire is the generated
  protobuf-es type; no hand-rolled mirror interfaces.
- Errors surface as `ConnectError` with the same stable `code` strings the
  CLI/MCP use (§6.5); UI branches on code, never message text.
- No component library; styles are hand-rolled in
  `src/styles/global.css` behind design tokens (light/dark via
  `data-theme` on `<html>`).
