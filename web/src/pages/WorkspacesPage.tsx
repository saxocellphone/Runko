import { useState } from "react";
import { Link } from "react-router-dom";
import { changesClient, publicBrowse, repoClient, workspacesClient } from "../api/client";
import { ChangeState } from "../gen/runko/v1/common_pb";
import { WorkspaceEventType, type WorkspaceEvent } from "../gen/runko/v1/workspaces_pb";
import {
  runBulk,
  selectAllState,
  toggled,
  visibleSelection,
  type BulkFailure,
} from "../lib/bulk";
import { absoluteTime, shortSha, timeAgo } from "../lib/format";
import { branchesForWorkspace, changesByOrigin } from "../lib/stacks";
import { useRpc } from "../lib/useRpc";
import { ActivityPresence, EmptyState, ErrorNote, InfoTip, Spinner } from "../components/ui";

const eventLabel: Record<number, string> = {
  [WorkspaceEventType.SNAPSHOT_PUSHED]: "snapshot",
  [WorkspaceEventType.CHANGE_PUSHED]: "change pushed",
  [WorkspaceEventType.CHANGE_LANDED]: "change landed",
  [WorkspaceEventType.CHANGE_ABANDONED]: "change abandoned",
  [WorkspaceEventType.WORKSPACE_CLOSED]: "closed",
};

// The list sorts on this, most recent first: the newest §12.6 timeline
// event, harness-reported activity when that's fresher (agents report
// between snapshots), or creation itself - a brand-new workspace is
// recent activity, not a bottom-of-the-list blank.
function activityKey(row: {
  lastEvent?: WorkspaceEvent;
  latestActivity?: { occurredAt: bigint };
  createdAt: bigint;
}) {
  return Math.max(
    Number(row.lastEvent?.occurredAt ?? 0),
    Number(row.latestActivity?.occurredAt ?? 0),
    Number(row.createdAt),
  );
}

export function WorkspacesPage() {
  const { data, error, loading, reload } = useRpc(async () => {
    // Open changes join to workspaces via their recorded push provenance
    // (§12.2): each workspace branch is expected to carry exactly one
    // stack, and this page is where that mapping is made visible.
    const [ws, open] = await Promise.all([
      workspacesClient.listWorkspaces({}),
      changesClient.listChanges({ state: ChangeState.OPEN }),
    ]);
    // Per-workspace enrichment, all in parallel: the newest timeline
    // event (the "last change" cell + the activity sort key) and the
    // Change that landed base_revision (CommitInfo.change_id), so Base
    // can link. Both are best-effort - a failure renders as "none yet"
    // / a plain sha, never as a broken page.
    const rows = await Promise.all(
      ws.workspaces.map(async (w) => {
        const [lastEvent, baseCommit] = await Promise.all([
          workspacesClient
            .listWorkspaceEvents({ id: w.id, pageSize: 1 })
            .then((r) => r.events[0], () => undefined),
          repoClient
            .listCommits({ rev: w.baseRevision, pageSize: 1 })
            .then((r) => r.commits[0], () => undefined),
        ]);
        return {
          w,
          lastEvent,
          latestActivity: w.latestActivity,
          createdAt: w.createdAt,
          baseChangeId: baseCommit?.changeId ?? "",
        };
      }),
    );
    rows.sort((a, b) => activityKey(b) - activityKey(a));
    return { rows, stacks: changesByOrigin(open.changes) };
  }, "workspaces");

  // Cleaning up after a batch of landed tasks is the common case (one
  // workspace per task, §12.2), so the list supports picking rows and
  // deleting them in one go. Selection is meaningless without the
  // delete verb, so the public read-only browse never renders it.
  const selectable = !publicBrowse;
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [busy, setBusy] = useState(false);
  const [failures, setFailures] = useState<BulkFailure[]>([]);

  const present = data?.rows.map(({ w }) => w.id) ?? [];
  const picked = visibleSelection(selected, present);
  const allState = selectAllState(selected, present);

  // Deletion refuses server-side while a workspace has open changes
  // (workspace_has_open_changes) and enforces owner-only, so a bulk run
  // partially failing is normal, not exceptional: attempt every pick,
  // then surface the server's own §6.5 message PER workspace instead of
  // one alert that loses which row refused. Single-row delete runs the
  // same path so both report identically.
  const runDelete = async (ids: string[]) => {
    const what = ids.length === 1 ? `workspace ${ids[0]}` : `${ids.length} workspaces`;
    if (
      !window.confirm(
        `Delete ${what}?\n\nRemoves each registry row and its snapshot refs. Open changes block deletion; local checkouts are not touched.`,
      )
    ) {
      return;
    }
    setBusy(true);
    setFailures([]);
    const { done, failed } = await runBulk(ids, (id) => workspacesClient.deleteWorkspace({ id }));
    setBusy(false);
    setFailures(failed);
    // Deleted rows leave the selection; anything that refused stays
    // picked, so a retry after landing/abandoning its changes is one
    // click and the note above the table says why it held.
    setSelected((prev) => {
      const next = new Set(prev);
      for (const id of done) next.delete(id);
      return next;
    });
    if (done.length > 0) reload();
  };

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Workspaces</h1>
        <p className="page-sub">
          Registry metadata only — content lives in Git at each snapshot ref (§12.2). Each
          branch carries one stack of changes.
        </p>
      </header>

      {loading && <Spinner />}
      {error && <ErrorNote error={error} />}
      {data && data.rows.length === 0 && <EmptyState>No workspaces yet.</EmptyState>}

      {failures.length > 0 && (
        <div className="bulk-failures" role="alert">
          <strong>
            {failures.length === 1 ? "1 workspace" : `${failures.length} workspaces`} not deleted
          </strong>
          <ul>
            {failures.map((f) => (
              <li key={f.id}>
                <span className="mono">{f.id}</span> — {f.message}
              </li>
            ))}
          </ul>
        </div>
      )}

      {selectable && picked.length > 0 && (
        <div className="bulk-bar">
          <span className="bulk-count">{picked.length} selected</span>
          <button
            className="btn btn-sm btn-danger"
            disabled={busy}
            onClick={() => void runDelete(picked)}
          >
            {busy ? "Deleting…" : "Delete selected"}
          </button>
          <button className="btn btn-sm" disabled={busy} onClick={() => setSelected(new Set())}>
            Clear
          </button>
        </div>
      )}

      {data && data.rows.length > 0 && (
        <section className="card">
          <table className="table">
            <thead>
              <tr>
                {selectable && (
                  <th className="check">
                    <input
                      type="checkbox"
                      aria-label="Select all workspaces"
                      checked={allState === "all"}
                      // Indeterminate is DOM-only state, not an attribute.
                      ref={(el) => {
                        if (el) el.indeterminate = allState === "some";
                      }}
                      disabled={busy}
                      onChange={() =>
                        setSelected(allState === "all" ? new Set() : new Set(present))
                      }
                    />
                  </th>
                )}
                <th>Workspace</th>
                <th>Owner</th>
                <th>
                  Last change
                  <InfoTip text="The newest event on this workspace's timeline (§12.6): a snapshot, a change push/land/abandon, close - or its creation, until anything else happens. The list sorts on this, most recently active first; the label links to the change when the event names one." />
                </th>
                <th>
                  Base
                  <InfoTip text="The trunk revision this workspace was last rebased onto - links to the change that landed it when the control plane knows the commit." />
                </th>
                <th>
                  Branches → stacks
                  <InfoTip text="Parallel lines of work inside this one workspace: each branch is a Git ref (refs/workspaces/<id>/<branch>) WIP is durably pushed to; 'head' is the default. One branch carries one stack - the open changes listed under each branch were pushed from it (recorded at push time, validated against this registry)." />
                </th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.rows.map(({ w, lastEvent, baseChangeId }) => {
                const branches = branchesForWorkspace(w.branches, data.stacks, w.id);
                return (
                <tr key={w.id} className={selected.has(w.id) ? "row-picked" : undefined}>
                  {selectable && (
                    <td className="check">
                      <input
                        type="checkbox"
                        aria-label={`Select workspace ${w.id}`}
                        checked={selected.has(w.id)}
                        disabled={busy}
                        onChange={() => setSelected((prev) => toggled(prev, w.id))}
                      />
                    </td>
                  )}
                  <td className="mono">
                    <Link to={`/workspaces/${w.id}`} title="Live WIP diff + activity timeline (§12.6)">
                      {w.id}
                    </Link>
                    {/* §12.6.1 at-a-glance: what the agent is doing right
                        now, straight off latest_activity while fresh. */}
                    <div>
                      <ActivityPresence ev={w.latestActivity} />
                    </div>
                  </td>
                  <td>{w.owner}</td>
                  <td>
                    <LastChange ev={lastEvent} createdAt={w.createdAt} />
                  </td>
                  <td className="mono">
                    {baseChangeId ? (
                      <Link
                        to={`/changes/${baseChangeId}`}
                        title="The change that landed this trunk revision"
                      >
                        {shortSha(w.baseRevision)}
                      </Link>
                    ) : (
                      shortSha(w.baseRevision)
                    )}
                  </td>
                  <td>
                    {branches.length === 0 && <span className="chip">none yet</span>}
                    {branches.map((b) => (
                      <BranchStack
                        key={b}
                        branch={b}
                        chains={data.stacks.get(`${w.id}/${b}`) ?? []}
                      />
                    ))}
                  </td>
                  <td>
                    {!publicBrowse && (
                      <button
                        className="btn btn-sm btn-danger"
                        title="Delete this workspace (registry row + snapshot refs). Refused while it has open changes."
                        disabled={busy}
                        onClick={() => void runDelete([w.id])}
                      >
                        Delete
                      </button>
                    )}
                  </td>
                </tr>
                );
              })}
            </tbody>
          </table>
        </section>
      )}
    </div>
  );
}

// LastChange is the newest §12.6 timeline event as one quiet cell:
// what happened and when, linking to the change for change_* events.
// A workspace with no events yet shows its creation - being born IS
// its last change (servers predating created_at send 0: "none yet").
function LastChange({ ev, createdAt }: { ev: WorkspaceEvent | undefined; createdAt: bigint }) {
  if (!ev) {
    if (Number(createdAt) > 0) {
      return <span title={absoluteTime(createdAt)}>created · {timeAgo(createdAt)}</span>;
    }
    return <span className="muted">none yet</span>;
  }
  const label = eventLabel[ev.type] ?? "event";
  const when = timeAgo(ev.occurredAt);
  if (ev.changeId) {
    return (
      <Link to={`/changes/${ev.changeId}`} title={absoluteTime(ev.occurredAt)}>
        {label} · {when}
      </Link>
    );
  }
  return <span title={absoluteTime(ev.occurredAt)}>{label} · {when}</span>;
}

// BranchStack renders one workspace branch with the stack(s) of open
// changes pushed from it - base-most at the bottom, derived with the SAME
// ancestry chaining the changes inbox uses, so the two views always
// agree. More than one chain means pre-invariant data (the funnel now
// enforces one stack per branch, §12.2) and is flagged as such.
function BranchStack({
  branch,
  chains,
}: {
  branch: string;
  chains: { id: string; title: string }[][];
}) {
  return (
    <div className="ws-branch">
      <span className="chip mono">{branch}</span>
      {chains.length > 1 && (
        <span className="chip chip-amber" title="This branch holds unrelated open changes - pushed before one-stack-per-branch was enforced. Land or abandon to reconcile.">
          {chains.length} split stacks
        </span>
      )}
      {chains.length === 0 ? (
        <span className="ws-branch-empty">no open changes</span>
      ) : (
        chains.map((stack, i) => (
          <ul className="ws-branch-stack" key={i}>
            {[...stack].reverse().map((c) => (
              <li key={c.id}>
                <Link className="ws-branch-change" to={`/changes/${c.id}`}>
                  {c.title}
                </Link>
              </li>
            ))}
          </ul>
        ))
      )}
    </div>
  );
}
