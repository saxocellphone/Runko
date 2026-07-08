# CLI contract

The CLI (`runko`, `runko-ci`) is the **primary agent interface** (docs/design.md
§8.3, decided 2026-07-07) - MCP is a thin remote adapter over the same
backing logic for clients that can't shell out, not the primary surface.
This document is the contract a script or agent can rely on: exit codes,
`--json` output, and the error shape, so scripting against these CLIs
doesn't require reading source to guess behavior.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success |
| `1` | The command was understood but failed - a structured error (see below) is printed to stderr |
| `2` | Usage error - unknown command, wrong/missing subcommand keyword, or unparseable flags |

Flag-parsing errors (bad flag names, malformed values) already exit `2` via
Go's stdlib `flag.ExitOnError`. A recognized command with a missing
*required* value (e.g. `project create` without `--name`) exits `1`, not
`2` - it parsed fine syntactically; the failure is a validation error, not
a usage error, matching how required-field errors are reported everywhere
else in this codebase (§6.5's `required_field` code).

## Structured errors (§6.5)

Every error this session's CLI code can identify a specific cause for -
"not a git repository", "detached HEAD", "unresolvable revision", "no
commits yet" - is surfaced via `internal/clierr.Error`:

```text
<message>
  -> <suggestion>
  see <doc_url>
```

printed to stderr, exit code `1`. This replaces git's raw `exit status 128`
passthrough for those specific, identifiable cases. Errors this session's
code has no more specific classification for (a failed `git clone` against
an unreachable remote, a genuine merge conflict) still surface git's own
message text - that IS the most useful information available for those
cases, not a gap to paper over with a generic wrapper.

## `--json` output

Every subcommand that produces data (not just a side-effect confirmation)
supports `--json`, emitting one JSON object to stdout instead of a human
summary line. Field names are the Go struct's exported field names
(capitalized) unless otherwise noted - this codebase does not yet generate
these shapes from `docs/spec/`, so treat field names as this table
documents them, not as guaranteed-stable API in the way `docs/spec/`
schemas are.

| Command | `--json` output |
|---|---|
| `runko doctor` | `DoctorReport` (`RepoDir`, `TrunkRef`, `HasRemote`, `RemoteName`, `RemoteURL`, `HasChangeIDHook`, `HooksDir`, `GitVersion`, `GitVersionOK`, `GitVersionError`, `IsJJWorkspace`, `JJChangeIDsWired`). In a jj workspace, `--install-hook` also sets `templates.commit_trailers` so Change-Id trailers derive from jj change ids (refuses to clobber a foreign trailers template: `jj_trailers_conflict`) |
| `runko project create` | `{"name", "path", "rev"}` |
| `runko project list` | `[]index.IndexedProject` (`Name`, `Path`, `Type`, `Capabilities`, `DeclaredDependencies`, `Visibility`, `Owners` `[{Ref, Source}]`, `RequiredChecks`) - needs a live runkod (`GET /api/projects`, the trunk-tip project index per §10.3), see §28.3 stage 12 |
| `runko change push` | `{"change_id", "ref"}`. Refuses when the tip is already on the remote trunk (`already_on_trunk`). In a jj workspace (colocated; `.jj` at the repo top level): the tip comes from jj's working copy (an empty, undescribed `@` is skipped in favor of `@-`), a missing trailer is a structured error (`jj_change_ids_not_configured` - the CLI never amends behind jj's back), and the push is by commit SHA since git HEAD is detached in colocated repos. One push updates EVERY Change in the pushed stack (series receive, §7.4) - after reworking the root of a stack, jj auto-rebases descendants and a single `change push` restacks the server |
| `runko change land` | `land.Outcome` (`Landed`, `LandedSHA`, `RequiresRevalidation`, `Conflicts`, `RaceRetry`) - needs a live runkod (`--runkod-url`/`--token`), unlike every other command in this table, see §13.5/§28.3 stage 11b. `--force` is the §13.5 admin override: bypasses owner/check gates and revalidation (server-authorized - admin principals and the deploy token only, 403 `force_denied` otherwise; never bypasses conflicts, stacked-parent ordering, or terminal states), audited durably as `landed_forced` |
| `runko change approve` | `MergeRequirements` (the same nested `{change_id, owners, checks, mergeable, blockers}` shape `GET .../merge-requirements` reports, per `docs/spec/mcp-tools/common.schema.json`) - needs a live runkod, see §13.5/§28.3 stage 11c |
| `runko change list` | `[]ChangeInfo` (`ChangeKey`, `State`, `BaseSHA`, `HeadSHA`, `GitRef`, `Title`, `LandedSHA`, `AuthoredBy`, `LandedBy`) - needs a live runkod (`GET /api/changes?state=`; `--state all` lists every state), see §28.3 stage 12c |
| `runko change abandon` | `ChangeInfo` - needs a live runkod; idempotent on an abandoned change, refuses a landed one (`invalid_state`), see §7.4/§28.3 stage 12c |
| `runko change rerun-check` | `MergeRequirements` (refreshed, same as approve) - needs a live runkod; resets a REQUIRED check to queued and emits `change.check_rerun_requested` for the org's CI plugin (§14.4.2), see §28.3 stage 12c |
| `runko agents-md` | `{"path"}` - also writes `AGENTS.md` (default; `--out` overrides) at the repo root, see §8.8/§28.3 stage 11 |
| `runko workspace create` | `WorkspaceInfo` (`ID`, `Owner`, `BaseRevision`, `ProjectAffinity`, `WriteAllowlist`, `SnapshotRef`, `Status`, `SparsePatterns`, `RepoPath`, `TrunkRef`) - needs a live runkod, see §12.3/§28.3 stage 12b |
| `runko workspace list` | `[]WorkspaceInfo` - needs a live runkod |
| `runko workspace attach` | `WorkspaceInfo` - needs a live runkod |
| `runko workspace snapshot` | `{"ref"}` - local git only (pushes to the worktree's workspace branch ref, `refs/workspaces/<id>/<branch>`; `head` is the default) |
| `runko auth login` | prints where the credential was stored; validates via `GET /api/whoami` first. Stores `{url, name?, secret}` at `$XDG_CONFIG_HOME/runko/credentials.json` when `XDG_CONFIG_HOME` is set (any platform), else the platform config dir (`~/.config/runko/` on Linux, `~/Library/Application Support/runko/` on macOS), 0600: with `--name` the secret is a principal password (sent as HTTP Basic - required for signed-up principals, whose passwords are hashed server-side), without it a bearer token. Every command taking `--runkod-url`/`--token` falls back to this stored credential |
| `runko auth status` | who the stored credential resolves to (live `whoami` round-trip) |
| `runko auth logout` | deletes the stored credential |
| `runko change create` | `{"change_id"}` - local git only: commits ALL working-tree changes as one commit carrying a fresh `Change-Id` trailer (§7.4); no auto-push - `change push` stays the explicit submit step |
| `runko change requirements` | `checks.MergeRequirements` - the §13.5 gates; `--change` defaults to HEAD's `Change-Id` trailer, needs a live runkod |
| `runko workspace branch` | `{"ref"}` - local git only (forks a parallel line: switches this worktree's snapshot target to `refs/workspaces/<id>/<name>` and snapshots the fork point, §12.2) |
| `runko workspace update-base` | `{"base_revision"}` - needs a live runkod (records the new base in the registry) |
| `runko-ci affected` | `affected.Result` (always JSON - no human mode exists for this command; it is CI-facing by design) |
| `runko-ci checkout` | `{"rev", "dest"}` |
| `runko-ci report-check` | `{"name", "status", "external_id"}` |

`runko mcp serve --runkod-url <url> --token <t>` is not in this table
because its stdout is not a `--json` data shape: it speaks newline-delimited
JSON-RPC 2.0 (the MCP stdio transport) until EOF, serving the six read-only
`"status": "v1"` tools from `docs/spec/mcp-tools/catalog.json` as thin
wrappers over the same runkod REST API the commands above use (§8.3, §17.4,
§28.3 stage 12). Its tool outputs conform to `docs/spec/mcp-tools/`
schemas - the schema-of-record convergence the section below describes,
already real for this surface.

Commands not listed here (`runko auth` - stubbed, needs a live control
plane not built in this environment) have no output contract yet.

## Single-contract rule with MCP (§8.3)

Where the MCP tool catalog (six read-only tools served by `runko mcp serve`
as of stage 12; the rest deferred to v1.x) and a CLI command cover the same
operation, they are meant to converge on the same wire shape -
`docs/spec/mcp-tools/` and `docs/spec/webhooks/` are the schema source both
should conform to. The CLI's hand-written JSON output above predates that
convergence for the commands that exist today; treat `docs/spec/` as the
schema of record once a given operation is generated from it, and this
table as the interim contract until then.
