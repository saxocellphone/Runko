---
name: runko-workspaces
description: Use BEFORE making any code change to this repository (Runko developed on Runko itself). The correct workspace/branch/change workflow - one workspace per workstream, one branch per stack, snapshot cadence, the jj-first submit/land loop, revalidation recovery, and what the server will refuse. Load it before creating a workspace, pushing a change, or landing.
---

# Working on Runko, through Runko

This repo's source of record is the prod runkod at
`https://runko.victornazzaro.com/o/runko/repo.git` (GitHub is a mirror -
never push it). Every change lands through the funnel: workspace-origin
push -> checks -> `runko change land`. The server ENFORCES what this
skill teaches; following it is the difference between one clean loop and
an afternoon of structured rejections.

## The model (get this right and everything else follows)

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
  per change: `jj new` between steps while working, `jj split` to carve
  up something that grew. One `runko change push` pushes the whole
  stack. Agent size caps are PER CHANGE - a monolith is refused where
  the same work as a stack passes - and smaller changes scope required
  checks narrower, so stacks genuinely land faster.
- **Stack only what depends; parallelize the rest (DAG, not line).**
  If step B doesn't build on step A, they belong on PARALLEL workspace
  branches (`runko workspace branch <name>`; jj: a separate
  `jj new 'main@origin'` line per independent change) - each reviews
  and lands on its own, neither waits for the other, and the workspace
  card renders the fork honestly. The push output nudges you when a
  stacked step touches nothing its parent touches.
- **One branch = one stack = one reviewable line.** `head` is the
  default. Parallel/unrelated work in the same workstream gets
  `runko workspace branch <name>`. The server refuses a second
  unrelated stack on one branch ("one branch, one stack") - the fix it
  suggests is real: restack, abandon, or branch.
- **Never claim a workspace you don't own or didn't create.** Origin
  claims are validated and owner-bound, and they drive the review UI's
  workspace cards. Two agents must NEVER share a workspace - even when
  they share the `admin` credential (where the server cannot tell them
  apart), each agent creates and uses its own.

## Setup (once per session)

    runko auth login --runkod-url https://runko.victornazzaro.com/o/runko --name admin
    runko workspace create --name <your-workstream> --by admin \
      --project <p> [--project <p2>] --clone-dir <shared> --dir <worktree>

Prefer working INSIDE the created worktree: the sparse cone stops
out-of-scope edits client-side, and `runko change push` stamps the
origin claim from the worktree's own config.

jj mode (colocated clones cannot live inside git worktrees): clone with
`jj git clone --colocate <remote>`, run `runko doctor --install-hook`
(wires Change-Id trailers to jj change ids), then bind it:
`git config runko.workspace <id>` + `git config runko.branch head`.
Fetch with plain `git fetch origin` (jj's own fetch fails SILENTLY on
URL-embedded basic auth), rebase with `jj rebase -d 'main@origin'`.

## The loop

1. Edit. **Snapshot often**: `runko workspace snapshot -m "wip"` -
   durable, secret-scanned; a killed session loses nothing.
2. Submit: `runko change create -m "<title>..."` (or `jj describe`),
   then `runko change push`. One push updates the WHOLE stack
   (series receive) - after editing a stack's root, jj auto-rebases
   descendants and one push restacks the server.
3. Gate: `runko change requirements --change <Id>` until mergeable.
   A stacked child is NOT mergeable until its parent lands - land
   bottom-up.
4. Land: `runko change land --change <Id> --repo <checkout>`. On
   "trunk has moved" (optimistic revalidation) it recovers BY ITSELF:
   sync onto trunk, re-push, wait for checks, retry (bounded; see
   --sync-timeout). `runko workspace sync` is the manual form, and
   `change push` already auto-syncs a stale base before pushing.
   Keep the change's touched-path footprint small: fewer required
   checks = a smaller race window.
5. Deploy (this repo): landing mirrors trunk to GitHub, Release images
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
