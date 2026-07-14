import { Link } from "react-router-dom";
import { ConnectError } from "@connectrpc/connect";
import { changesClient, publicBrowse, repoClient, workspacesClient } from "../api/client";
import { ChangeState, WorkspaceStatus } from "../gen/runko/v1/common_pb";
import { WorkspaceEventType, type WorkspaceEvent } from "../gen/runko/v1/workspaces_pb";
import { absoluteTime, shortSha, timeAgo } from "../lib/format";
import { branchesForWorkspace, changesByOrigin } from "../lib/stacks";
import { useRpc } from "../lib/useRpc";
import { ActivityPresence, EmptyState, ErrorNote, InfoTip, Spinner } from "../components/ui";

const statusLabel: Record<number, string> = {
  [WorkspaceStatus.ACTIVE]: "active",
  [WorkspaceStatus.DETACHED]: "detached",
  [WorkspaceStatus.CLOSED]: "closed",
};

const eventLabel: Record<number, string> = {
  [WorkspaceEventType.SNAPSHOT_PUSHED]: "snapshot",
  [WorkspaceEventType.CHANGE_PUSHED]: "change pushed",
  [WorkspaceEventType.CHANGE_LANDED]: "change landed",
  [WorkspaceEventType.CHANGE_ABANDONED]: "change abandoned",
  [WorkspaceEventType.WORKSPACE_CLOSED]: "closed",
};

// The list sorts on this, most recent first: the newest §12.6 timeline
// event, or the newest harness-reported activity when that's fresher
// (agents report between snapshots).
function activityKey(row: { lastEvent?: WorkspaceEvent; latestActivity?: { occurredAt: bigint } }) {
  return Math.max(
    Number(row.lastEvent?.occurredAt ?? 0),
    Number(row.latestActivity?.occurredAt ?? 0),
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
          baseChangeId: baseCommit?.changeId ?? "",
        };
      }),
    );
    rows.sort((a, b) => activityKey(b) - activityKey(a));
    return { rows, stacks: changesByOrigin(open.changes) };
  }, "workspaces");

  // Deletion refuses server-side while the workspace has open changes
  // (workspace_has_open_changes) and enforces owner-only - surface the
  // server's own §6.5 message rather than pre-judging client-side.
  const onDelete = async (id: string) => {
    if (!window.confirm(`Delete workspace ${id}?\n\nRemoves the registry row and its snapshot refs. Open changes block deletion; local checkouts are not touched.`)) {
      return;
    }
    try {
      await workspacesClient.deleteWorkspace({ id });
      reload();
    } catch (err) {
      window.alert(ConnectError.from(err).rawMessage);
    }
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
      {data && data.rows.length > 0 && (
        <section className="card">
          <table className="table">
            <thead>
              <tr>
                <th>Workspace</th>
                <th>Owner</th>
                <th>
                  Last change
                  <InfoTip text="The newest event on this workspace's timeline (§12.6): a snapshot or a change push/land/abandon. The list sorts on this, most recently active first; the label links to the change when the event names one." />
                </th>
                <th>
                  Base
                  <InfoTip text="The trunk revision this workspace was last rebased onto - links to the change that landed it when the control plane knows the commit." />
                </th>
                <th>
                  Branches → stacks
                  <InfoTip text="Parallel lines of work inside this one workspace: each branch is a Git ref (refs/workspaces/<id>/<branch>) WIP is durably pushed to; 'head' is the default. One branch carries one stack - the open changes listed under each branch were pushed from it (recorded at push time, validated against this registry)." />
                </th>
                <th>Status</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {data.rows.map(({ w, lastEvent, baseChangeId }) => {
                const branches = branchesForWorkspace(w.branches, data.stacks, w.id);
                return (
                <tr key={w.id}>
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
                    <LastChange ev={lastEvent} />
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
                    <span
                      className={`chip ${w.status === WorkspaceStatus.ACTIVE ? "chip-green" : ""}`}
                    >
                      {statusLabel[w.status] ?? "unknown"}
                    </span>
                  </td>
                  <td>
                    {!publicBrowse && (
                      <button
                        className="btn btn-sm btn-danger"
                        title="Delete this workspace (registry row + snapshot refs). Refused while it has open changes."
                        onClick={() => void onDelete(w.id)}
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
function LastChange({ ev }: { ev: WorkspaceEvent | undefined }) {
  if (!ev) return <span className="muted">none yet</span>;
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
