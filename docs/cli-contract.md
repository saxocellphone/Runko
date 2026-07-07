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
| `runko doctor` | `DoctorReport` (`RepoDir`, `TrunkRef`, `HasRemote`, `RemoteName`, `RemoteURL`, `HasChangeIDHook`, `HooksDir`, `GitVersion`, `GitVersionOK`, `GitVersionError`) |
| `runko project create` | `{"name", "path", "rev"}` |
| `runko change push` | `{"change_id", "ref"}` |
| `runko agents-md` | `{"path"}` - also writes `AGENTS.md` (default; `--out` overrides) at the repo root, see §8.8/§28.3 stage 11 |
| `runko-ci affected` | `affected.Result` (always JSON - no human mode exists for this command; it is CI-facing by design) |
| `runko-ci checkout` | `{"rev", "dest"}` |
| `runko-ci report-check` | `{"name", "status", "external_id"}` |

Commands not listed here (`runko auth`, `workspace`, `mcp` - stubbed, need a
live control plane not built in this environment) have no output contract
yet.

## Single-contract rule with MCP (§8.3)

Where the (deferred, v1.x) MCP tool catalog and a CLI command cover the same
operation, they are meant to converge on the same wire shape -
`docs/spec/mcp-tools/` and `docs/spec/webhooks/` are the schema source both
should conform to. The CLI's hand-written JSON output above predates that
convergence for the commands that exist today; treat `docs/spec/` as the
schema of record once a given operation is generated from it, and this
table as the interim contract until then.
