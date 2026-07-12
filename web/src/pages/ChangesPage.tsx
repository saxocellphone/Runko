import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ConnectError } from "@connectrpc/connect";
import { authUser, changesClient } from "../api/client";
import { ChangeState, type ChangeSummary, type MergeRequirements } from "../gen/runko/v1/common_pb";
import { inAttention } from "../lib/comments";
import { absoluteTime, changeNumberLabel, shortChangeId, timeAgo } from "../lib/format";
import {
  buildWorkspaceCards,
  HOME_BRANCH,
  layoutForest,
  layoutStack,
  stackHasFork,
  stackSize,
  TRUNK_NODE_ID,
  type StackLayout,
  type WorkspaceCard,
} from "../lib/stacks";
import { useRpc } from "../lib/useRpc";
import { RailGraphRow, RailGraphTrunk } from "../components/RailGraph";
import {
  AttentionChip,
  AuthorChip,
  ChecksChip,
  EmptyState,
  ErrorNote,
  MergeableChip,
  OriginChip,
  ReviewChip,
  Spinner,
  StateBadge,
} from "../components/ui";

const tabs = [
  { state: ChangeState.OPEN, label: "Open" },
  { state: ChangeState.LANDED, label: "Landed" },
  { state: ChangeState.ABANDONED, label: "Abandoned" },
] as const;

// One history page. Open is NOT paginated: it is the working set (bounded
// by active workspaces), and the stack cards need every open change to
// derive parent links - a page boundary through a stack would draw broken
// trees. Landed/abandoned grow with history and page server-side.
const PAGE_SIZE = 25;

export function ChangesPage() {
  const [tab, setTab] = useState<ChangeState>(ChangeState.OPEN);

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Changes</h1>
        <p className="page-sub">Stacked changes across the monorepo, trunk at the bottom.</p>
      </header>

      <div className="tabs">
        {tabs.map((t) => (
          <button
            key={t.state}
            className={`tab${tab === t.state ? " active" : ""}`}
            onClick={() => setTab(t.state)}
          >
            {t.label}
          </button>
        ))}
      </div>

      {tab === ChangeState.OPEN ? (
        <OpenInbox />
      ) : (
        <PagedFlatList
          key={tab}
          state={tab}
          label={tabs.find((t) => t.state === tab)?.label.toLowerCase() ?? ""}
        />
      )}
    </div>
  );
}

function OpenInbox() {
  const { data, error, loading } = useRpc(async () => {
    const res = await changesClient.listChanges({ state: ChangeState.OPEN });
    // The open inbox also needs abandoned changes: one that a pending
    // change still depends on stays VISIBLE (struck through) until
    // nothing depends on it (2026-07-09).
    let abandoned: ChangeSummary[] = [];
    try {
      abandoned = (await changesClient.listChanges({ state: ChangeState.ABANDONED })).changes;
    } catch {
      // Retention degrades: orphans fall back to the amber anchor.
    }
    // One merge-requirements call per open change powers the status chips
    // and stack dots. Only this inbox pays it - fanning the calls out on
    // the landed tab burned ~400ms of unused gate computation per row
    // (stage 15 dogfood). A batch RPC is still the follow-up if the open
    // inbox itself outgrows this (noted in proto/README.md).
    const reqs = new Map<string, MergeRequirements>();
    await Promise.all(
      res.changes.map(async (c) => {
        try {
          const r = await changesClient.getMergeRequirements({ changeId: c.id });
          if (r.requirements) reqs.set(c.id, r.requirements);
        } catch {
          // Chips degrade to "unknown"; the list itself still renders.
        }
      }),
    );
    return { changes: res.changes, abandoned, requirements: reqs };
  }, "changes-open");

  return (
    <>
      {loading && <Spinner />}
      {error && <ErrorNote error={error} />}
      {data && <AttentionInbox changes={data.changes} requirements={data.requirements} />}
      {data && (
        <StackedList changes={data.changes} abandoned={data.abandoned} requirements={data.requirements} />
      )}
      {data && data.changes.length === 0 && <EmptyState>No open changes.</EmptyState>}
    </>
  );
}

// AttentionInbox is §17.2's "owner attention inbox", driven by the derived
// set (§13.4.2): the open changes whose turn is YOURS - requested of you,
// or owned by you and unreviewed at the current head, or yours and already
// answered. Renders only for a signed-in named principal; the anonymous
// operator/dev loop has no identity to match.
function AttentionInbox({
  changes,
  requirements,
}: {
  changes: ChangeSummary[];
  requirements: Map<string, MergeRequirements>;
}) {
  if (!authUser) return null;
  const mine = changes.filter((c) =>
    inAttention(requirements.get(c.id)?.attentionSet ?? [], authUser),
  );
  if (mine.length === 0) return null;
  return (
    <section className="card attention-inbox">
      <header className="stack-card-head">
        <span>Needs your attention · {mine.length}</span>
      </header>
      {mine.map((c) => (
        <div className="stack-row" key={c.id}>
          <span className="rail">
            <span className="dot dot-review" />
          </span>
          <div className="change-line">
            <Link className="change-title-link" to={`/changes/${c.id}`}>
              {c.title}
            </Link>
            <span className="change-meta">
              <span>{changeNumberLabel(c.number)}</span>
              <AuthorChip author={c.authoredBy} />
            </span>
          </div>
          <span className="change-chips">
            <AttentionChip requirements={requirements.get(c.id)} you={authUser} />
          </span>
        </div>
      ))}
    </section>
  );
}

// PagedFlatList: server-side pagination for the history tabs (stage 15 -
// "don't show all changes in one page"). Pages accumulate behind a Load
// more button; the offset token comes from the server, never derived
// client-side.
function PagedFlatList({ state, label }: { state: ChangeState; label: string }) {
  const [changes, setChanges] = useState<ChangeSummary[]>([]);
  const [nextToken, setNextToken] = useState("");
  const [loading, setLoading] = useState(true);
  const [loadingMore, setLoadingMore] = useState(false);
  const [error, setError] = useState<ConnectError | undefined>(undefined);

  const fetchPage = useCallback(
    (pageToken: string) => changesClient.listChanges({ state, pageSize: PAGE_SIZE, pageToken }),
    [state],
  );

  useEffect(() => {
    let stale = false;
    setLoading(true);
    setError(undefined);
    fetchPage("")
      .then((res) => {
        if (stale) return;
        setChanges(res.changes);
        setNextToken(res.nextPageToken);
        setLoading(false);
      })
      .catch((err: unknown) => {
        if (stale) return;
        setError(ConnectError.from(err));
        setLoading(false);
      });
    return () => {
      stale = true;
    };
  }, [fetchPage]);

  const loadMore = async () => {
    setLoadingMore(true);
    try {
      const res = await fetchPage(nextToken);
      setChanges((prev) => [...prev, ...res.changes]);
      setNextToken(res.nextPageToken);
    } catch (err) {
      setError(ConnectError.from(err));
    }
    setLoadingMore(false);
  };

  if (loading) return <Spinner />;
  if (error) return <ErrorNote error={error} />;
  if (changes.length === 0) return <EmptyState>No {label} changes.</EmptyState>;
  return (
    <>
      <FlatList changes={changes} />
      {nextToken && (
        <div className="load-more">
          <button className="btn btn-sm" onClick={() => void loadMore()} disabled={loadingMore}>
            {loadingMore ? "Loading…" : "Load more"}
          </button>
        </div>
      )}
    </>
  );
}

function StackedList({
  changes,
  abandoned,
  requirements,
}: {
  changes: ChangeSummary[];
  abandoned: ChangeSummary[];
  requirements: Map<string, MergeRequirements>;
}) {
  const cards = buildWorkspaceCards(changes, abandoned);
  return (
    <div>
      {cards.map((card, i) => (
        <WorkspaceStackCard key={card.workspace ?? `loose-${i}`} card={card} requirements={requirements} />
      ))}
    </div>
  );
}

// WorkspaceStackCard: ONE card per workspace (2026-07-09). Its branches
// render as a tree sharing the main anchor - fork lanes, playground
// style; abandoned ancestors a pending change still depends on stay
// visible, struck through, until nothing depends on them. Roots whose
// base is genuinely unreachable (parent landed as a different commit,
// or vanished) sit below with the amber anchor.
function WorkspaceStackCard({
  card,
  requirements,
}: {
  card: WorkspaceCard;
  requirements: Map<string, MergeRequirements>;
}) {
  const size = [...card.roots, ...card.stranded].reduce((n, r) => n + stackSize(r), 0);
  const forked = card.roots.length > 1 || card.roots.some(stackHasFork);
  const layout = card.roots.length > 0 ? layoutForest(card.roots) : null;
  return (
    <section className="card stack-card">
      {(card.workspace || size > 1) && (
        <header className="stack-card-head">
          <span>
            {size > 1 ? `Stack · ${size} changes${forked ? " · branched" : ""}` : "Stack · 1 change"}
          </span>
          {card.workspace && <OriginChip workspace={card.workspace} branch="" />}
        </header>
      )}
      {layout && layout.rows.map((row, i) => <StackRow key={row.change.id} row={row} rowIndex={i} layout={layout} requirements={requirements} />)}
      {card.stranded.map((root) => {
        const sub = layoutStack(root);
        return (
          <div key={root.change.id}>
            {sub.rows.map((row, i) => (
              <StackRow key={row.change.id} row={row} rowIndex={i} layout={sub} requirements={requirements} />
            ))}
            <div className="stack-row stack-row-trunk">
              <RailGraphTrunk lanes={sub.lanes} />
              <div className="change-line anchor-warn" title="This stack's base commit is not on trunk - its parent change landed as a different commit or is gone. Rebase onto trunk and re-push.">
                ⚠ not on main
              </div>
              <span />
            </div>
          </div>
        );
      })}
    </section>
  );
}

function StackRow({
  row,
  rowIndex,
  layout,
  requirements,
}: {
  row: StackLayout["rows"][number];
  rowIndex: number;
  layout: StackLayout;
  requirements: Map<string, MergeRequirements>;
}) {
  const c = row.change;
  if (c.id === TRUNK_NODE_ID) {
    return (
      <div className="stack-row stack-row-trunk">
        <RailGraphRow layout={layout} rowIndex={rowIndex} change={c} trunk />
        <div className="change-line">main</div>
        <span />
      </div>
    );
  }
  const isAbandoned = c.state === ChangeState.ABANDONED;
  return (
    <div className={`stack-row${isAbandoned ? " stack-row-abandoned" : ""}`}>
      <RailGraphRow layout={layout} rowIndex={rowIndex} change={c} requirements={requirements.get(c.id)} />
      <div className="change-line">
        <Link className="change-title-link" to={`/changes/${c.id}`}>
          {c.title}
        </Link>
        <span className="change-meta">
          <span>{changeNumberLabel(c.number)}</span>
          <span className="mono">{shortChangeId(c.id)}</span>
          <AuthorChip author={c.authoredBy} />
          {c.originBranch && c.originBranch !== HOME_BRANCH && (
            <OriginChip workspace={c.originWorkspace} branch={c.originBranch} branchOnly />
          )}
        </span>
      </div>
      <span className="change-chips">
        {isAbandoned ? (
          <StateBadge state={c.state} />
        ) : (
          <>
            <MergeableChip requirements={requirements.get(c.id)} />
            <AttentionChip requirements={requirements.get(c.id)} you={authUser} />
            <ReviewChip requirements={requirements.get(c.id)} />
            <ChecksChip requirements={requirements.get(c.id)} />
          </>
        )}
      </span>
    </div>
  );
}

function FlatList({ changes }: { changes: ChangeSummary[] }) {
  if (changes.length === 0) return null;
  return (
    <section className="card">
      {changes.map((c) => (
        <div className="stack-row" key={c.id}>
          <span className="rail">
            <StateBadgeDot state={c.state} />
          </span>
          <div className="change-line">
            <Link className="change-title-link" to={`/changes/${c.id}`}>
              {c.title}
            </Link>
            <span className="change-meta">
              <span>{changeNumberLabel(c.number)}</span>
              <span className="mono">{shortChangeId(c.id)}</span>
              <AuthorChip author={c.authoredBy} />
              {c.landedAt > 0n && (
                <span title={absoluteTime(c.landedAt)}>landed {timeAgo(c.landedAt)}</span>
              )}
            </span>
          </div>
          <span className="change-chips">
            <StateBadge state={c.state} />
          </span>
        </div>
      ))}
    </section>
  );
}

function StateBadgeDot({ state }: { state: ChangeState }) {
  // Landed is history, not health: it gets the accent dot that matches
  // badge-landed, never the green that means "active" elsewhere.
  const cls = state === ChangeState.LANDED ? "dot-landed" : "";
  return <span className={`dot ${cls}`} />;
}
