import { useState } from "react";
import { Link, useParams } from "react-router-dom";
import { changesClient, publicBrowse, workspacesClient } from "../api/client";
import { ChangeState, WorkspaceStatus } from "../gen/runko/v1/common_pb";
import { WorkspaceEventType, type WorkspaceEvent } from "../gen/runko/v1/workspaces_pb";
import type { WorkspaceActivityEvent } from "../gen/runko/v1/common_pb";
import {
  ACTIVITY_KINDS,
  countByKind,
  kindMeta,
  normalizeKind,
  parseStoredKinds,
  type ActivityKind,
} from "../lib/activity";
import { absoluteTime, shortSha, timeAgo } from "../lib/format";
import { branchesForWorkspace, changesByOrigin } from "../lib/stacks";
import { useRpc } from "../lib/useRpc";
import { useWatch, type WatchState } from "../lib/useWatch";
import { DiffView } from "../components/DiffView";
import {
  ActivityPresence,
  AuthorChip,
  BackLink,
  EmptyState,
  ErrorNote,
  InfoTip,
  Spinner,
} from "../components/ui";

const statusLabel: Record<number, string> = {
  [WorkspaceStatus.ACTIVE]: "active",
  [WorkspaceStatus.DETACHED]: "detached",
  [WorkspaceStatus.CLOSED]: "closed",
};

// WorkspacePage is the §12.6 live view: what is this workspace's agent (or
// human) doing RIGHT NOW - the per-branch WIP diff of its snapshot tip vs
// base, plus the stats-only activity timeline. Liveness is stream-as-poke:
// WatchWorkspace frames just fire reload() on the same unary useRpc hooks
// a plain page load uses.
export function WorkspacePage() {
  const { workspaceId = "" } = useParams();
  const [branchChoice, setBranchChoice] = useState("");
  // Kind filter for the Agent activity card (§12.6.1, decided 2026-07-14):
  // all-visible by default, the choice sticks per browser (the theme's
  // localStorage pattern) - an agent's command firehose stays hideable
  // without silently under-reporting on first visit.
  const [activityKinds, setActivityKinds] = useState<Set<ActivityKind>>(
    () => new Set(parseStoredKinds(window.localStorage.getItem("runko-activity-kinds"))),
  );
  const toggleActivityKind = (k: ActivityKind) => {
    setActivityKinds((prev) => {
      const next = new Set(prev);
      if (next.has(k)) next.delete(k);
      else next.add(k);
      window.localStorage.setItem(
        "runko-activity-kinds",
        JSON.stringify(ACTIVITY_KINDS.filter((x) => next.has(x))),
      );
      return next;
    });
  };

  const meta = useRpc(async () => {
    const [w, open] = await Promise.all([
      workspacesClient.getWorkspace({ id: workspaceId }),
      changesClient.listChanges({ state: ChangeState.OPEN }),
    ]);
    return { workspace: w.workspace!, stacks: changesByOrigin(open.changes) };
  }, workspaceId);

  const branches = meta.data
    ? branchesForWorkspace(meta.data.workspace.branches, meta.data.stacks, workspaceId)
    : [];
  const branch = branchChoice || (branches.includes("head") ? "head" : (branches[0] ?? "head"));

  const diff = useRpc(
    () => workspacesClient.getWorkspaceDiff({ id: workspaceId, branch }),
    `${workspaceId}/${branch}/diff`,
  );
  // The server caps each workspace's timeline (§12.6 retention), so one
  // generous page covers the rail; no pager.
  const events = useRpc(
    () => workspacesClient.listWorkspaceEvents({ id: workspaceId, pageSize: 100 }),
    `${workspaceId}/events`,
  );
  // The §12.6.1 harness-reported feed - capped server-side like the
  // timeline, so one page covers the card.
  const activity = useRpc(
    () => workspacesClient.listWorkspaceActivity({ id: workspaceId, pageSize: 100 }),
    `${workspaceId}/activity`,
  );

  const live = useWatch(publicBrowse ? "" : workspaceId, () => {
    meta.reload();
    diff.reload();
    events.reload();
    activity.reload();
  });

  const back = <BackLink to="/workspaces">Workspaces</BackLink>;
  if (meta.loading) return <div className="page">{back}<Spinner /></div>;
  if (meta.error) return <div className="page">{back}<ErrorNote error={meta.error} /></div>;
  if (!meta.data) return null;
  const ws = meta.data.workspace;

  // Filtering is render-time over the already-fetched page: counts and
  // rows recompute for free on every poke-driven refetch.
  const activityEvents = activity.data?.events ?? [];
  const activityCounts = countByKind(activityEvents);
  const visibleActivity = activityEvents.filter((ev) => activityKinds.has(normalizeKind(ev.kind)));

  return (
    <div className="page">
      {back}
      <header className="page-header" data-tour="ws-header">
        <h1 className="page-title">
          <span className="mono">{ws.id}</span>
          <span className={`chip ${ws.status === WorkspaceStatus.ACTIVE ? "chip-green" : ""}`}>
            {statusLabel[ws.status] ?? "unknown"}
          </span>
          <LiveDot state={live} />
        </h1>
        <div className="change-meta-row">
          <span>{ws.owner}</span>
          <span className="mono" title={`base ${ws.baseRevision}`}>
            base {shortSha(ws.baseRevision)}
          </span>
          <InfoTip text="The trunk revision this workspace's WIP is diffed against - what its next stack forks from (§12.2)." />
          <span className="chip-row">
            {ws.projectAffinity.map((p) => (
              <Link className="chip" key={p} to={`/projects/${p}`}>
                {p}
              </Link>
            ))}
          </span>
          <ActivityPresence ev={activity.data?.events[0]} />
        </div>
      </header>

      <div className="change-layout">
        <div data-tour="wip-diff">
          {branches.length > 1 && (
            <div className="tabs">
              {branches.map((b) => (
                <button
                  key={b}
                  className={`tab ${b === branch ? "active" : ""}`}
                  onClick={() => setBranchChoice(b)}
                >
                  {b}
                </button>
              ))}
            </div>
          )}
          {diff.loading && <Spinner />}
          {diff.error && <ErrorNote error={diff.error} />}
          {diff.data && diff.data.snapshotSha === "" && (
            <EmptyState>
              No snapshot on <span className="mono">{branch}</span> yet — WIP appears here the
              moment the workspace pushes one (<span className="mono">runko workspace watch</span>{" "}
              keeps it continuous, §12.6).
            </EmptyState>
          )}
          {diff.data && diff.data.snapshotSha !== "" && (
            <>
              <div className="change-meta-row">
                <span>
                  WIP at <span className="mono">{shortSha(diff.data.snapshotSha)}</span> vs base
                </span>
                <InfoTip text="The branch's snapshot tip diffed against the workspace base - the work in flight BEFORE any change is pushed for review. Snapshots amend in place, so this is always the current state, not history." />
              </div>
              {diff.data.files.length === 0 ? (
                <EmptyState>
                  Snapshot matches the base — nothing in flight on{" "}
                  <span className="mono">{branch}</span>.
                </EmptyState>
              ) : (
                <DiffView files={diff.data.files} />
              )}
            </>
          )}
        </div>

        <aside>
          <section className="card side-card" data-tour="agent-activity">
            <h2>
              Agent activity
              <InfoTip text="What the workspace's agent reports it is doing - reads, edits, commands - live, before anything is pushed (§12.6.1). Client-claimed and observability-only: it never feeds gates. Wire it up with `runko agent hooks`." />
            </h2>
            {activity.loading && <Spinner />}
            {activity.error && <ErrorNote error={activity.error} />}
            {activity.data && activity.data.events.length === 0 && (
              <EmptyState>
                Nothing reported yet — wire the harness with{" "}
                <span className="mono">runko agent hooks</span> and reads, edits, and commands
                appear here live.
              </EmptyState>
            )}
            {activity.data && activity.data.events.length > 0 && (
              <>
                <div className="chip-row activity-filter">
                  {ACTIVITY_KINDS.map((k) => (
                    <button
                      key={k}
                      className={`chip ${activityKinds.has(k) ? "" : "chip-off"}`}
                      onClick={() => toggleActivityKind(k)}
                      title={`${activityKinds.has(k) ? "hide" : "show"} ${kindMeta[k].label} events`}
                    >
                      {kindMeta[k].glyph} {kindMeta[k].label}
                      <span className="chip-count">{activityCounts[k]}</span>
                    </button>
                  ))}
                </div>
                {visibleActivity.length === 0 ? (
                  <EmptyState>Everything is filtered out — toggle a kind back on.</EmptyState>
                ) : (
                  <ul className="ws-timeline">
                    {visibleActivity.map((ev) => (
                      <ActivityRow key={String(ev.id)} ev={ev} />
                    ))}
                  </ul>
                )}
              </>
            )}
          </section>

          <section className="card side-card" data-tour="ws-timeline">
            <h2>
              Activity
              <InfoTip text="Stats-only timeline recorded at receive/land time (§12.6): snapshots, change pushes, lands, abandons, closure. Line counts are numstat totals; file content never leaves Git." />
            </h2>
            {events.loading && <Spinner />}
            {events.error && <ErrorNote error={events.error} />}
            {events.data && events.data.events.length === 0 && (
              <EmptyState>Nothing recorded yet.</EmptyState>
            )}
            {events.data && events.data.events.length > 0 && (
              <ul className="ws-timeline">
                {events.data.events.map((ev) => (
                  <TimelineRow key={String(ev.id)} ev={ev} />
                ))}
              </ul>
            )}
          </section>
        </aside>
      </div>
    </div>
  );
}

function LiveDot({ state }: { state: WatchState }) {
  const label: Record<WatchState, string> = {
    live: "live — updates stream in as the workspace works",
    connecting: "connecting to the live feed…",
    offline: "live feed unreachable — showing the last loaded state, retrying",
  };
  return (
    <span className={`live-dot live-dot-${state}`} title={label[state]}>
      <span className="live-dot-pip" />
      {state}
    </span>
  );
}

const eventLabel: Record<number, string> = {
  [WorkspaceEventType.SNAPSHOT_PUSHED]: "snapshot",
  [WorkspaceEventType.CHANGE_PUSHED]: "change pushed",
  [WorkspaceEventType.CHANGE_LANDED]: "landed",
  [WorkspaceEventType.CHANGE_ABANDONED]: "abandoned",
  [WorkspaceEventType.WORKSPACE_CLOSED]: "workspace closed",
};

// ActivityRow is one §12.6.1 harness-reported event: kind + who + when,
// with the detail (a path, a command line) on its own mono line.
function ActivityRow({ ev }: { ev: WorkspaceActivityEvent }) {
  const kind = normalizeKind(ev.kind);
  return (
    <li className="ws-timeline-row">
      <div className="ws-timeline-head">
        <span className="chip">
          {kindMeta[kind].glyph} {kindMeta[kind].label}
        </span>
        {ev.actor && <AuthorChip author={ev.actor} />}
        <span className="spacer" />
        <span title={absoluteTime(ev.occurredAt)}>{timeAgo(ev.occurredAt)}</span>
      </div>
      <div className="ws-timeline-body">
        <span className="mono ws-activity-detail" title={ev.detail}>
          {ev.detail}
        </span>
      </div>
    </li>
  );
}

function TimelineRow({ ev }: { ev: WorkspaceEvent }) {
  const isChange = ev.changeId !== "";
  return (
    <li className="ws-timeline-row">
      <div className="ws-timeline-head">
        <span className={`chip ${ev.type === WorkspaceEventType.CHANGE_LANDED ? "chip-green" : ""}`}>
          {eventLabel[ev.type] ?? "event"}
        </span>
        {ev.branch && <span className="chip mono">{ev.branch}</span>}
        <span className="spacer" />
        <span title={absoluteTime(ev.occurredAt)}>{timeAgo(ev.occurredAt)}</span>
      </div>
      <div className="ws-timeline-body">
        {ev.actor && <AuthorChip author={ev.actor} />}
        {isChange ? (
          <Link className="mono" to={`/changes/${ev.changeId}`} title={ev.changeId}>
            {ev.changeId.slice(0, 13)}…
          </Link>
        ) : (
          ev.sha && (
            <span className="mono" title={ev.sha}>
              {shortSha(ev.sha)}
            </span>
          )
        )}
        {ev.type === WorkspaceEventType.SNAPSHOT_PUSHED && (
          <span>
            {ev.filesChanged} file{ev.filesChanged === 1 ? "" : "s"}{" "}
            <span className="added-count">+{ev.additions}</span>{" "}
            <span className="deleted-count">−{ev.deletions}</span>
          </span>
        )}
      </div>
    </li>
  );
}
