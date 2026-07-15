import { Fragment } from "react";
import { Link } from "react-router-dom";
import type { ChangeSummary, MergeRequirements } from "../gen/runko/v1/common_pb";
import { changeNumberLabel } from "../lib/format";
import { buildStackForest, layoutStack } from "../lib/stacks";
import { RailGraphRow, RailGraphTrunk } from "./RailGraph";

// The Graphite-style stack panel: upstack changes on top, trunk-most
// change at the bottom, one status dot per change. `stack` arrives as
// GetChangeStack's flat tree (parents before children); forks - e.g. two
// workspace branches building on one base (§12.2) - render as extra lanes
// merging into their parent, git-log-graph style.
//
// Deliberately NO trunk terminator row (2026-07-15): "main" at the bottom
// read as "based on main's tip", which is exactly what a behind base is
// not - the same misread the behind-tip chip exists for (2026-07-11). The
// change header's land-cleanliness chip answers "what happens when this
// lands" (§13.5) instead. Only the stranded anchor keeps a row: a root
// whose base is not on trunk at all is a warning, not wallpaper.
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
  return (
    <nav className="rail-list">
      {forest.map((root) => {
        const layout = layoutStack(root);
        const stranded = !root.change.baseOnTrunk;
        return (
          <Fragment key={root.change.id}>
            {layout.rows.map(({ change: c }, i) => (
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
                  terminal={!stranded && i === layout.rows.length - 1}
                />
                <span className="rail-item-body">
                  <div className="rail-item-title">{c.title}</div>
                  <div className="rail-item-sub">{changeNumberLabel(c.number)}</div>
                </span>
              </Link>
            ))}
            {stranded && (
              <div className="rail-item trunk">
                <RailGraphTrunk lanes={layout.lanes} />
                <span
                  className="rail-item-body anchor-warn"
                  title="This stack's base commit is not on trunk - rebase onto trunk and re-push."
                >
                  ⚠ base not on trunk
                </span>
              </div>
            )}
          </Fragment>
        );
      })}
    </nav>
  );
}
