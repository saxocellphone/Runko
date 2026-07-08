import { useState } from "react";
import { Link } from "react-router-dom";
import { changesClient } from "../api/client";
import { ChangeState, type ChangeSummary, type MergeRequirements } from "../gen/runko/v1/common_pb";
import { changeNumberLabel, shortChangeId } from "../lib/format";
import { buildStackForest, flattenStack, stackSize, type StackNode } from "../lib/stacks";
import { useRpc } from "../lib/useRpc";
import {
  AuthorChip,
  ChecksChip,
  EmptyState,
  ErrorNote,
  MergeableChip,
  ReviewChip,
  Spinner,
  StateBadge,
  StatusDot,
  TrunkIcon,
} from "../components/ui";

const tabs = [
  { state: ChangeState.OPEN, label: "Open" },
  { state: ChangeState.LANDED, label: "Landed" },
  { state: ChangeState.ABANDONED, label: "Abandoned" },
] as const;

export function ChangesPage() {
  const [tab, setTab] = useState<ChangeState>(ChangeState.OPEN);

  const { data, error, loading } = useRpc(async () => {
    const res = await changesClient.listChanges({ state: tab });
    // One merge-requirements call per change powers the status chips and
    // stack dots. Fine at demo scale; a batch RPC is the obvious follow-up
    // if the real list view needs it (noted in proto/README.md).
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
    return { changes: res.changes, requirements: reqs };
  }, `changes-${tab}`);

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

      {loading && <Spinner />}
      {error && <ErrorNote error={error} />}
      {data && tab === ChangeState.OPEN && (
        <StackedList changes={data.changes} requirements={data.requirements} />
      )}
      {data && tab !== ChangeState.OPEN && <FlatList changes={data.changes} />}
      {data && data.changes.length === 0 && (
        <EmptyState>
          No {tabs.find((t) => t.state === tab)?.label.toLowerCase()} changes.
        </EmptyState>
      )}
    </div>
  );
}

function StackedList({
  changes,
  requirements,
}: {
  changes: ChangeSummary[];
  requirements: Map<string, MergeRequirements>;
}) {
  const forest = buildStackForest(changes);
  return (
    <div>
      {forest.map((root) => (
        <StackCard key={root.change.id} root={root} requirements={requirements} />
      ))}
    </div>
  );
}

function StackCard({
  root,
  requirements,
}: {
  root: StackNode;
  requirements: Map<string, MergeRequirements>;
}) {
  const rows = flattenStack(root);
  const size = stackSize(root);
  const hasFork = (n: StackNode): boolean => n.children.length > 1 || n.children.some(hasFork);
  const forked = hasFork(root);
  return (
    <section className="card stack-card">
      {size > 1 && (
        <header className="stack-card-head">
          Stack · {size} changes{forked ? " · forked" : ""}
        </header>
      )}
      {rows.map(({ change: c, depth }, i) => (
        <div className="stack-row" key={c.id} style={{ paddingLeft: depth * 18 }}>
          <span className={`rail${i > 0 ? " rail-up" : ""} rail-down`}>
            <StatusDot requirements={requirements.get(c.id)} state={c.state} />
          </span>
          <div className="change-line">
            <Link className="change-title-link" to={`/changes/${c.id}`}>
              {c.title}
            </Link>
            <span className="change-meta">
              <span>{changeNumberLabel(c.number)}</span>
              <span className="mono">{shortChangeId(c.id)}</span>
              <AuthorChip author={c.authoredBy} />
            </span>
          </div>
          <span className="change-chips">
            <MergeableChip requirements={requirements.get(c.id)} />
            <ReviewChip requirements={requirements.get(c.id)} />
            <ChecksChip requirements={requirements.get(c.id)} />
          </span>
        </div>
      ))}
      <div className="stack-row stack-row-trunk">
        <span className="rail rail-up">
          <TrunkIcon />
        </span>
        <div className="change-line">main</div>
        <span />
      </div>
    </section>
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
  const cls = state === ChangeState.LANDED ? "dot-ready" : "";
  return <span className={`dot ${cls}`} />;
}
