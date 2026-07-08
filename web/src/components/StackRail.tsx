import { Link } from "react-router-dom";
import type { ChangeSummary, MergeRequirements } from "../gen/runko/v1/common_pb";
import { changeNumberLabel } from "../lib/format";
import { buildStackForest, flattenStack, type StackRow } from "../lib/stacks";
import { StatusDot, TrunkIcon } from "./ui";

// The Graphite-style stack panel: upstack changes on top, trunk at the
// bottom, one status dot per change. `stack` arrives as GetChangeStack's
// flat tree (parents before children); forks - e.g. two workspace branches
// building on one base (§12.2) - render as indented sibling lines.
export function StackRail({
  stack,
  currentId,
  requirementsById,
}: {
  stack: ChangeSummary[];
  currentId: string;
  requirementsById: Map<string, MergeRequirements>;
}) {
  const rows: StackRow[] = buildStackForest(stack).flatMap(flattenStack);
  return (
    <nav className="rail-list">
      {rows.map(({ change: c, depth }) => (
        <Link
          key={c.id}
          to={`/changes/${c.id}`}
          className={`rail-item${c.id === currentId ? " current" : ""}`}
          style={{ marginLeft: depth * 14 }}
        >
          <span className="rail rail-up rail-down">
            <StatusDot requirements={requirementsById.get(c.id)} state={c.state} />
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
