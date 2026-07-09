import { Link } from "react-router-dom";
import type { ChangeSummary, MergeRequirements } from "../gen/runko/v1/common_pb";
import { changeNumberLabel } from "../lib/format";
import { buildStackForest, layoutStack } from "../lib/stacks";
import { RailGraphRow, RailGraphTrunk } from "./RailGraph";

// The Graphite-style stack panel: upstack changes on top, trunk at the
// bottom, one status dot per change. `stack` arrives as GetChangeStack's
// flat tree (parents before children); forks - e.g. two workspace branches
// building on one base (§12.2) - render as extra lanes merging into their
// parent, git-log-graph style.
export function StackRail({
  stack,
  currentId,
  requirementsById,
}: {
  stack: ChangeSummary[];
  currentId: string;
  requirementsById: Map<string, MergeRequirements>;
}) {
  const forest = buildStackForest(stack);
  const lanes = Math.max(1, ...forest.map((root) => layoutStack(root).lanes));
  return (
    <nav className="rail-list">
      {forest.map((root) => {
        const layout = layoutStack(root);
        return layout.rows.map(({ change: c }, i) => (
          <Link
            key={c.id}
            to={`/changes/${c.id}`}
            className={`rail-item${c.id === currentId ? " current" : ""}`}
          >
            <RailGraphRow
              layout={layout}
              rowIndex={i}
              change={c}
              requirements={requirementsById.get(c.id)}
            />
            <span className="rail-item-body">
              <div className="rail-item-title">{c.title}</div>
              <div className="rail-item-sub">{changeNumberLabel(c.number)}</div>
            </span>
          </Link>
        ));
      })}
      <div className="rail-item trunk">
        <RailGraphTrunk lanes={lanes} />
        {stack.length > 0 && !stack[0]!.baseOnTrunk ? (
          <span className="rail-item-body anchor-warn" title="This stack's base commit is not on trunk - rebase onto trunk and re-push.">
            ⚠ not on main
          </span>
        ) : (
          <span className="rail-item-body">main</span>
        )}
      </div>
    </nav>
  );
}
