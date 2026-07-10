# Build-graph adapter contract (design.md §14.5.4)

Pre-session-1-style blocker for DAG stages 9b/9c (design.md §26 #13, §28.4).
This is the spec artifact that unblocks implementation, same role as
`project.schema.json` played for stage 3 or the webhook schemas for stage 8:
decided here, transcribed there.

**Division of responsibility (§14.2, §14.5.4) stays intact.** The platform's
own affected computation (`affected.Compute`, paths + declared deps) is the
*floor* - correct with zero build tooling, and never required (NG4). A build
graph, when the org has one, *refines* that floor to target-level precision.
The daemon (`runkod`, stage 10) never executes customer build tooling; the
adapter runs **runner-side only**, inside `runko-ci`.

## 1. Engine interface (Go side, implemented in `platform/buildadapter/`)

```go
// Engine queries one build system's dependency graph. Implementations shell
// out to the engine's own query tool - never reimplement graph semantics.
type Engine interface {
    // Query returns the targets affected by changedPaths within universe,
    // evaluated hermetically at whatever revision repoDir is checked out to.
    // Implementations MUST treat any ambiguity (a path outside the build
    // graph's view, a query timeout, a version mismatch) as an error rather
    // than guessing - Refine() below is what turns an error into fail-closed
    // run_everything, so Engine itself should never silently under-report.
    Query(ctx context.Context, req QueryRequest) (QueryResult, error)
}

type QueryRequest struct {
    RepoDir         string   // working tree checked out at head_sha (§14.4.4's checkout contract)
    UniversePattern string   // e.g. "//..."
    ChangedPaths    []string
    Timeout         time.Duration
}

type QueryResult struct {
    Targets []string // e.g. "//commerce/checkout:go_default_test"
}
```

`Refine(ctx, engine, req, projects []ProjectInfo) Refinement` is the
fail-closed wrapper every call site uses instead of calling `Engine.Query`
directly:

| Refine behavior | Condition |
|---|---|
| `Refinement{Targets: [...], RunEverything: false}` | `Query` returns cleanly |
| `Refinement{RunEverything: true, Reason: "..."}` | `Query` returns a non-nil error, **for any reason** (query failure, non-zero exit, timeout, unparseable output, engine binary missing) |

No partial-success path exists. This is §14.5.3's fail-closed rule applied
to the adapter layer: an engine that half-answered is treated exactly like
one that didn't answer at all, because the alternative (trusting a possibly-
truncated target set to gate a merge) is the one mistake this contract exists
to prevent.

## 2. CLI invocation (`runko-ci affected --engine <name>`)

```
runko-ci affected --base <sha> --head <sha> --engine bazel [--universe //...] [--engine-timeout 60s]
```

- `--engine` is optional; omitting it is the floor-only path stages 5/9
  already ship (project-level `affected.Result`, no build graph consulted).
- When set, `runko-ci` additionally resolves `req.RepoDir` (whatever checkout
  it's already operating on - no second clone), builds `ChangedPaths` from
  the same diff it computed for the floor, and calls `Refine`.
- Output is the existing `affected.Result` JSON (stage 9) plus one additional
  top-level field, `build_refinement`, present only when `--engine` was
  passed:

```json
{
  "projects": [{"name": "checkout-api", "path": "commerce/checkout"}],
  "run_everything": false,
  "reason_codes": ["direct_path"],
  "build_refinement": {
    "engine": "bazel",
    "targets": ["//commerce/checkout:go_default_test"],
    "run_everything": false
  }
}
```

If the engine itself fails closed, `build_refinement.run_everything` is
`true` and the top-level `run_everything` is forced `true` too (an engine
failure escalates the whole computation, never just its own sub-field) -
this mirrors how a root-invalidation pattern already escalates
`affected.Result` today.

## 3. Refinement post-back (`affected-refinement.schema.json`)

Once a Change exists in the control plane (stage 10+), `runko-ci` may POST
the same refinement as an *addition* to the Change, shown alongside the
platform's project-level computation - never replacing it. `Refine`'s output
maps directly onto `affected-refinement.schema.json` in this directory.
Whether check-set policies key on projects (default) or on this refined
target set is an **org policy choice** (§14.5.4) - the post-back is additive
information either way, not itself an enforcement decision.

Delivery is a plain bearer-token POST, the same shape `runko-ci report-check`
already uses (§14.6) - no new transport primitive.

## 4. Engine admission criteria (recap, §14.5.4)

An engine qualifies only if it provides all three:

1. **Declared** targets (explicit BUILD/BUCK-class files)
2. **Hermetic evaluation at a SHA** (same checkout ⇒ same graph)
3. **A reverse-dependency query** (`rdeps`-equivalent)

Bazel and Buck2 qualify. Task runners (Make, Turborepo/Nx, npm scripts)
structurally never qualify (non-hermetic, package-coarse) - the platform
floor is their permanent affected story, which is also what keeps NG4 honest.

## 5. Bazel query recipe (v1 implementation)

Changed file paths are converted to Bazel source-file labels
(`a/b/c.go` → `//a/b:c.go` - a source file's label is always
`//<dir>:<basename>`), then queried in one shot:

```sh
bazel query \
  --output=label \
  --noshow_progress --order_output=no \
  "rdeps(${UNIVERSE}, set(${CHANGED_FILE_LABELS}))"
```

- `${UNIVERSE}` defaults to `//...`; orgs may narrow it (large monorepos with
  disjoint Bazel workspaces under one Git repo).
- `set(...)` takes the changed-file labels directly - Bazel resolves each to
  whatever target(s) reference it as a source, then `rdeps` walks the
  reverse-dependency closure within the universe.
- `--output=label` gives one label per line, directly usable as
  `QueryResult.Targets`; no `--output=json` parsing needed for v1.
- A changed file with **no** referencing target (e.g. a stray file, or one
  outside any `BUILD` package) is not an error from Bazel's side - it simply
  contributes nothing to the `rdeps` set. This is intentionally treated as a
  normal "no additional targets from this path" result, not a query failure;
  the platform floor (`affected.Compute`) is what actually gates on paths
  with no owning project, per §14.5.3's fail-closed rule.
- Nonzero exit, a query timeout, or unparseable output all map to `Refine`'s
  fail-closed path (table in §1) - `platform/buildadapter/bazel` never tries to
  distinguish "this specific failure is probably fine."

An optional second query (`cquery` with `--output=jsonproto`) can resolve
each target's owning `BUILD` package back to a Runko project path for the
`target_projects` field in the refinement schema; v1 does this with the same
longest-path-prefix match `affected.Compute` already uses to own a changed
path (a target's package directory is matched the same way a changed file's
directory is), rather than a second Bazel invocation, since project
boundaries are already known from `index.Scan`.

## 6. Buck2 mapping notes (contract-shaped from day one, not implemented yet)

Buck2's `buck2 uquery` exposes the same shape:

```sh
buck2 uquery "rdeps(${UNIVERSE}, set(${CHANGED_FILE_LABELS}))" --output-format=list
```

- Buck2 target syntax (`//dir:name`) and source-file-as-label resolution
  match Bazel's closely enough that `QueryRequest`/`QueryResult` need no
  changes - only the shelled-out binary and its flag names differ.
- `buck2 uquery` (unconfigured query) is the `rdeps`-equivalent to reach for,
  not `cquery` (configured, i.e. post-build-settings) - v1's Bazel recipe
  above uses plain `query` for the same reason: universe-wide reverse-
  dependency queries don't need configuration resolution, only structural
  graph traversal.
- No timeline commitment; this section exists so a second implementation
  proves the interface (§19.4) without a contract redesign when it lands.

## 7. What this contract deliberately does not cover

- RBE / remote caching (BuildBuddy, Namespace, EngFlow) - customer-side,
  consumes this adapter's target sets, never wired into Runko (§14.7 Tier 3).
- Any daemon-side execution of `bazel`/`buck2` - runner-side only, always
  (§9, §14.5.4). `runkod` (stage 10) receives the post-back; it never shells
  out to a build tool itself.
- A generic "build system plugin" mechanism beyond the two engines above -
  admission is by criterion (§4), not an open plugin marketplace, at least
  through v1.
