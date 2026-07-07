import { Link } from "react-router-dom";
import type { ChangeSummary, MergeRequirements } from "../gen/runko/v1/common_pb";
import { changeNumberLabel } from "../lib/format";
import { StatusDot, TrunkIcon } from "./ui";

// The Graphite-style stack panel: upstack changes on top, trunk at the
// bottom, one status dot per change. `stack` arrives trunk-most first
// (GetChangeStack's order) and is rendered reversed.
export function StackRail({
  stack,
  currentId,
  requirementsById,
}: {
  stack: ChangeSummary[];
  currentId: string;
  requirementsById: Map<string, MergeRequirements>;
}) {
  const topFirst = [...stack].reverse();
  return (
    <nav className="rail-list">
      {topFirst.map((c) => (
        <Link
          key={c.id}
          to={`/changes/${c.id}`}
          className={`rail-item${c.id === currentId ? " current" : ""}`}
        >
          <span className="rail rail-up rail-down">
            <StatusDot requirements={requirementsById.get(c.id)} />
          </span>
          <span className="rail-item-body">
            <div className="rail-item-title">{c.title}</div>
            <div className="rail-item-sub">{changeNumberLabel(c.number)}</div>
          </span>
        </Link>
      ))}
      <div className="rail-item trunk">
        <span className="rail rail-up">
          <TrunkIcon />
        </span>
        <span className="rail-item-body">main</span>
      </div>
    </nav>
  );
}
