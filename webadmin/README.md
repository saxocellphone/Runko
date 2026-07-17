# webadmin — the deployment admin panel

The operator-facing admin panel, a **separate project from `web/`** with
a deliberately separate failure domain:

- **Its own sign-in flow.** Operators authenticate against the dedicated
  `GET /api/admin/whoami` (runkod/orghub.go) with an operator credential
  (config principal or deploy token) — never the org-scoped login. The
  credential lives under its own browser-storage keys, so an org session
  in the main app and an operator session here never mix.
- **Its own API surface.** Everything the panel does rides `/api/admin/*`
  only: the org estate (archived included), operator org creation
  (not gated by `--allow-org-create`), and the archive lifecycle.
- **Its own pod.** A static SPA under `/admin` served by its own nginx
  (see `nginx.conf`, `Dockerfile`), so the panel stays reachable when the
  main web app is down; the header's health strip probes the
  unauthenticated `/healthz` + `/readyz` continuously, so when **runkod**
  is down the panel renders the outage instead of a blank error. (The
  admin *API* still lives in runkod — a fully independent admin daemon
  was considered and deferred: archive/create take effect in the serving
  daemon's memory, so a second writer needs DB-authoritative org routing
  first.)

Stack: React + TS + Vite, no router, no Connect — the admin surface is
plain JSON over fetch. `npm run check` = tsc + oxlint + vite build.
Dev loop: `VITE_RUNKO_URL=http://localhost:8080/ npm run dev` against a
local runkod.
