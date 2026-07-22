---
name: runko-workspaces
description: Use BEFORE making any code change to this repository (Runko developed on Runko itself). The correct workspace/branch/change workflow - one workspace per workstream, one branch per stack, snapshot cadence, the runko-first submit/land loop (jj is surgical-only), revalidation recovery, and what the server will refuse. Load it before creating a workspace, pushing a change, or landing.
---

# Working on Runko, through Runko

This repo's source of record is the prod runkod at
`https://runko.victornazzaro.com/o/runko/repo.git` (GitHub is a mirror -
never push it). Every change lands through the funnel: workspace-origin
push -> checks -> `runko change land`. The server ENFORCES what this
skill teaches; following it is the difference between one clean loop and
an afternoon of structured rejections.

## The model (get this right and everything else follows)

- **Arm automerge instead of polling.** After `runko change push`, run
  `runko change automerge --change <id>` and move on to the next task -
  the server lands the change the moment its checks and approvals go
  green, attributed to you, surviving amends. Background poll-and-land
  loops are the anti-pattern this verb deletes. (`runko change land`
  stays for when you need the synchronous recovery loop - revalidation
  re-push - or want to watch it happen.)

- **Step zero: work under a TASK identity.** If you're holding a human
  or admin credential, demote yourself before anything else:
  `runko agent create --task <slug>` mints `agent-<slug>-<x>` with a
  token (shown once, dies by TTL - default 8h). Use that name:token for
  the git remote and every `--token` from then on. Attribution, agent
  policy, workspace ownership, and auto-close all follow the task
  identity; ten concurrent agents = ten identities, zero shared
  credentials. (An agent credential cannot mint - no self-replication.)
- **One workspace = one task.** Start every new task with
  `runko workspace create`; when the task's changes have all landed or
  been abandoned, the workspace is DONE - `runko workspace delete <id>`
  cleans it up (refused while changes are still open). Agent-owned
  workspaces close automatically at that point and the server refuses
  any further push into them, so reusing yesterday's workspace is a
  dead end, not a shortcut. A task can still hold several changes
  (a stack) and parallel branches - that's what branches are for.
- **Stack small changes; never push one big one.** One reviewable step
  per change: a fresh `runko change create -m ...` per step stacks
  naturally (`jj split` is the surgical fix when one grew too big). One
  `runko change push` pushes the whole stack. Agent size caps are PER
  CHANGE - a monolith is refused where the same work as a stack passes -
  and smaller changes scope required checks narrower, so stacks
  genuinely land faster.
- **Stack only what depends; parallelize the rest (DAG, not line).**
  If step B doesn't build on step A, they belong on PARALLEL workspace
  branches (`runko workspace branch <name>`) - each reviews and lands
  on its own, neither waits for the other, and the workspace card
  renders the fork honestly. The push output nudges you when a stacked
  step touches nothing its parent touches.
- **One branch = one stack = one reviewable line.** `head` is the
  default. Parallel/unrelated work in the same workstream gets
  `runko workspace branch <name>`. The server refuses a second
  unrelated stack on one branch ("one branch, one stack") - the fix it
  suggests is real: restack, abandon, or branch.
- **New project? Scaffold with the verb, never by hand.** `runko
  project create --name <n> --type <t> --lang <l> --repo "$(runko
  workspace path <ws>)"` is the §10 intent->files pipeline (this verb has
  no `-w`; `--repo` is how it reaches a checkout): it generates
  PROJECT.yaml (build capability +
  target patterns), README, a minimal BUILD.bazel, and a stub entrypoint.
  Do NOT hand-write a manifest by copying a sibling project - that's the
  anti-pattern the pipeline exists to delete, and it's how two services
  in a row (watchdog, mailer) got hand-carved. Then evolve the scaffold
  in ordinary changes: add the §14.9.1 `ci.checks` block (the template
  deliberately omits it; the merge gate reads ci.checks, not the build
  capability) and let real code + gazelle supersede the stubs. Agent
  caveat: PROJECT.yaml is an owners surface - an agent push carrying it
  (and anything under .github/workflows/) is REFUSED, so the manifest
  lands as a separate operator-lane change, AFTER the code change its
  checks point at.
- **Never claim a workspace you don't own or didn't create.** Origin
  claims are validated and owner-bound, and they drive the review UI's
  workspace cards. Two agents must NEVER share a workspace - even when
  they share the `admin` credential (where the server cannot tell them
  apart), each agent creates and uses its own.

## Setup (once per session)

    runko auth login --runkod-url https://runko.victornazzaro.com/o/runko --name admin
    runko workspace create --name <your-workstream> --by admin \
      --project <p> [--project <p2>]

## The worktree is transparent - address workspaces by NAME

**Never `cd` into a workspace worktree, and never tell a human to.** The
materialization is an implementation detail (§12.7); the workspace NAME
is the handle. `-w <name[@branch]>` runs a verb against that workspace's
registered materialization from ANYWHERE - the repo root included:

    runko change create -w <ws> -m "<what and why>"
    runko change push -w <ws>
    runko change requirements -w <ws>
    runko workspace watch -w <ws> &
    runko workspace snapshot -w <ws>
    runko workspace sync -w <ws>
    runko agent hooks --install -w <ws>

The full `-w` set: `change` create/amend/push/requirements/land/describe/
comment/comments/resolve/request-review, `workspace`
snapshot/watch/branch/sync, and `agent hooks`. Two groups deliberately
lack it: the server-side verbs (`automerge`, `approve`, `abandon`,
`list`) key off the Change-Id and never needed a checkout at all, while
`project create`, `doctor`, and `agents-md` reach a checkout only through
`--repo` - for those, `--repo "$(runko workspace path <ws>)"` is the
transparent form. Passing `-w` together with a non-`.` `--dir`/`--repo`
is a contradiction and is refused.

Editing files is the one thing that happens at a path: use the worktree
path for Read/Write/Edit, and keep every `runko` invocation `-w`. When
you genuinely need the directory (a shell one-liner, a build), ask for
it - `runko workspace path <ws>` - rather than hardcoding or `cd`-ing.

The reason it matters: a `cd` moves the whole session into a checkout
that is a *different* materialization of the same repo, and everything
after it silently inherits that cwd - builds, greps, unrelated commands.
Handing that path to a human is worse: it leaks a private detail of how
Runko materializes work and trains them out of the flag that exists to
hide it. Sparse-cone protection and the origin claim `change push`
stamps come from the WORKSPACE, not from your cwd, so `-w` gives up
nothing.

jj is SURGICAL-ONLY (§21, repositioned 2026-07-11): the basic loop -
create/push/land/snapshot - is runko in every checkout. Reach for jj
when you need mid-stack rework (`jj edit`/`jj squash`; descendants
auto-rebase), `jj split`, or history diagnosis. If you want a colocated
checkout, runko sets it up (2026-07-16, §12.7): pass `--jj` to
`workspace create` or `attach` - a STANDALONE full clone (jj cannot
colocate inside a git worktree), trailer template + binding + hooks all
wired; no `--clone-dir` (there is no shared store in this mode). In it,
`workspace snapshot` is automatically out-of-band and `workspace
branch` refuses (a parallel line = a second checkout via
`attach --jj --branch <name>`). Fetch with plain `git fetch origin`
(jj's own fetch fails SILENTLY on URL-embedded basic auth); sync is
`runko workspace sync` as everywhere (jj-aware rebase).

## Worktree setup & gotchas

- **Transport is stamped for you.** `workspace create`/`attach` write
  `http.postBuffer=524288000` and `http.version=HTTP/1.1` into the
  worktree, so the first push behind Cloudflare no longer dies with a
  bare `HTTP 400 ... unexpected disconnect while reading sideband
  packet`. If you DO hit it (a checkout made before this), that is
  TRANSPORT not policy - `runko change push` now says so and names the
  one-line remedy (`git config http.postBuffer 524288000 &&
  git config http.version HTTP/1.1`); re-run.
- **Identity is bound to the worktree - no XDG juggling.** `workspace
  create` stamps `runko.owner`, and the no-`XDG_CONFIG_HOME` agent form
  is one command: `runko workspace create --by agent-x --as agent-x
  --token <tok>` (admin mints the token with `runko agent create`; `--as`
  authenticates AS the agent for this command without storing or
  clobbering a login). `-w` resolves that same binding, so authoring
  from the repo root picks up the agent identity exactly as authoring
  inside the worktree would.
- **The cone holds only your `--project` dirs.** Reading or compiling
  across projects needs `git sparse-checkout add <dir>` in the worktree
  first (`platform internal runkod proto db` covers a Go build). That is
  safe: affinity gates the paths a push TOUCHES, never what you
  materialized to read.
- **Never build binaries into the worktree.** `go build` with no `-o`
  drops a multi-MB executable at the repo root, and `change create`
  stages the WHOLE tree - that is how a 7.5MB junk blob (plus phantom
  size + affinity errors) once rode into a change. `change create` now
  REFUSES large/executable untracked files by name; remove or
  `.gitignore` them, and build to a scratch dir: `go build -o /tmp/x
  ./...` (or just `go vet ./...`). `--allow-large` is the escape hatch
  for an intentional binary asset.
- **Affinity is by PROJECT NAME, not directory.** `runko project list`
  names them: `cli/runko-ci` is project `cli`; a repo-root file like
  `runko-ci` belongs to the ROOT project (`repo`), not a `runko-ci`
  directory. The affinity rejection now spells out the path, where it
  maps, and the current affinity set.
- **Guarding `$GIT_COMMON_DIR/info/exclude`? ANCHOR with a leading `/`.**
  An unanchored `runko-ci` line also excludes the `cli/runko-ci/`
  directory and silently drops new source from the commit; write
  `/runko-ci` to mean the root file only.
- **Reword/extend a change with `runko change amend`**, not raw
  `git commit --amend` (which fails on a checkout with no configured git
  author): it folds the working tree into HEAD's Change, keeps the
  Change-Id, and carries the Runko identity fallback.

## The loop

1. Edit. **Stream as you work**: start `runko workspace watch -w <ws> &`
   once at task start - out-of-band auto-snapshots (durable,
   secret-scanned; a killed session loses nothing) and the workspace
   page follows WIP live. Wire the activity feed once per workspace with
   `runko agent hooks --install -w <ws>` (needs RUNKO_RUNKOD_URL/
   RUNKO_TOKEN in the harness env, or a stored login).
   `runko workspace snapshot -w <ws> -m "wip"` remains the manual form.
   The server nudges the first change push from a workspace that never
   streamed - following this bullet keeps pushes quiet.
2. **Added, moved, or deleted a Go file? Run `bazel run //:gazelle`
   BEFORE committing** - BUILD.bazel files are generated, a stale srcs
   list fails the required `bazel-check` gate, and this is the single
   most common avoidable check failure in this repo. `make check-bazel`
   reproduces the whole gate locally (build + drift + adapter test)
   when you want certainty before pushing.
3. Submit: `runko change create -w <ws> -m "<title>..."`, then
   `runko change push -w <ws>`. One push updates the WHOLE stack (series
   receive) - after surgically editing a stack's root with jj, jj
   auto-rebases descendants and one push restacks the server.
4. Gate: `runko change requirements --change <Id>` until mergeable.
   A stacked child is NOT mergeable until its parent lands - land
   bottom-up.
5. Land: `runko change land --change <Id> -w <ws>`. On "trunk has
   moved" (optimistic revalidation) it recovers BY ITSELF: sync onto
   trunk, re-push, wait for checks, retry (bounded; see
   --sync-timeout). `runko workspace sync -w <ws>` is the manual form,
   and `change push` already auto-syncs a stale base before pushing.
   Keep the change's touched-path footprint small: fewer required
   checks = a smaller race window.
6. Deploy (this repo): landing mirrors trunk to GitHub, Release images
   builds, then `kubectl rollout restart deploy/{runkod,runko-web}
   -n maas-dev` - wait for the release run OF YOUR LANDED SHA before
   restarting, and verify /readyz after.

## What the server will refuse (don't fight it)

- refs/for push with no workspace origin (`changes are born in
  workspaces`). Bootstrap of an empty org is the only exemption.
- A second unrelated stack on one branch.
- Landing a child before its parent; landing anything abandoned;
  re-pushing a landed Change-Id (start fresh work with a new Change).
- Agent principals: pushes outside workspace affinity, self-approval,
  force-land - always.

## Hygiene

- Abandon dead changes (`runko change abandon`) - an abandoned change
  stays in the inbox only while something still stacks on it.
- jj identity gotcha: a landed change's jj change id is terminal. For
  follow-up work in the same colocated clone, `jj new 'main@origin'`
  FIRST; if you contaminated the working copy, `jj duplicate @` gives
  the content a fresh identity.
- Raw git is for transport only (fetch, the bound-clone plumbing).
  Verbs that exist - use them: snapshot, branch, attach, sync,
  change create/push/land/abandon/requirements.
