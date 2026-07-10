# Runko web frontend

The web UI (docs/design.md §17.2), built against the Connect contract in
`proto/runko/v1/` via [Connect-ES](https://connectrpc.com/) — see
proto/README.md for the server side. Design language is inspired by
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
- **Browse** — repo explorer (`RepoService`: lazy directory tree + file
  viewer, deep-linkable as `/browse/<path>`, project-badged via
  longest-prefix ownership) with Code/Blame/History tabs: path-scoped
  history (renames followed), age-tinted blame gutter, and rows linking to
  the *Change* (state-badged) rather than the raw commit.
- **New project** — the §10.1 create flow: name + type (+ optional
  owners), live preview of the generated files, and "Create as a change" —
  creation opens an ordinary Change (trunk is closed, §6.9); landing it is
  what makes the project real.
- **Projects / project detail / workspaces / code search** — thinner reads
  over the corresponding services (the workspaces page derives per-branch
  stacks with the same grouping the inbox uses).
- **Org support** — sidebar org switcher (rebases the Connect transport
  onto `/o/<org>/`), org-scoped sign-in, `/settings` (description,
  org-required checks, member management), and an operator-only Admin
  page (org estate, archive lifecycle, create-org). Public orgs render
  read-only without a session under GitHub-style `/<org>` URLs.

## Transport: real vs. demo

`src/api/client.ts` picks the transport once per page load:

- **`/demo/*`** — always the in-memory fake transport (`src/api/fake/`),
  regardless of configuration: a coherent demo scene through the same
  generated types, with real mutation semantics (approve/land/rerun/
  abandon) so every flow is exercisable. `main.tsx` mounts the app under
  the `/demo` basename, so in-app navigation stays inside the demo; the
  sidebar badge cross-links back to the live app.
- **everywhere else with `VITE_RUNKO_URL`** (`/` = same-origin, or an
  absolute URL for the dev loop) — Connect protocol against runkod's
  connect-go handlers (`runkod/rpc.go`). Auth is a **sign-in gate**:
  name + password of a runkod named principal (`--principal
  name=…;token=…`; the password IS the registry token), validated via
  `GET /api/whoami` and stored per-browser. The signed-in identity drives
  approve/land attribution server-side; the deploy token as password
  gives an anonymous session. `VITE_RUNKO_TOKEN=<deploy token>` remains a
  build-time anonymous-bearer fallback for the local dev loop ONLY —
  never set it on a published image. The daemon's RPC + whoami routes
  answer CORS preflights, so the Vite dev server (or any origin) can talk
  to it directly.
- **everywhere else, unset** — the fake transport serves the root app
  too, with the "Demo data — set VITE_RUNKO_URL" badge.

`scripts/fullstack.mjs` drives the whole stack in headless Chromium
(inbox → change page → approve → land → projects/browse/workspaces →
/demo isolation) against a seeded runkod; see its header for the exact
setup it expects.

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
