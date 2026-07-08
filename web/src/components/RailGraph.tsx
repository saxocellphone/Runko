import type { MergeRequirements, ChangeSummary } from "../gen/runko/v1/common_pb";
import { railCells, type StackLayout } from "../lib/stacks";
import { StatusDot, TrunkIcon } from "./ui";

// The graph gutter for one stack row: fixed-width lane cells drawn with
// absolutely-positioned line segments, so verticals line up across rows
// no matter how the surrounding row is laid out (git-log-graph style).
export function RailGraphRow({
  layout,
  rowIndex,
  change,
  requirements,
}: {
  layout: StackLayout;
  rowIndex: number;
  change: ChangeSummary;
  requirements: MergeRequirements | undefined;
}) {
  return (
    <span className="rail-graph" style={{ width: layout.lanes * LANE_W }}>
      {railCells(layout, rowIndex).map((cell, lane) => (
        <span className="rgc" key={lane}>
          {cell.kind === "v" && <span className="rg-v" />}
          {cell.kind === "h" && <span className="rg-h" />}
          {cell.kind === "corner" && (
            <>
              <span className="rg-v-top" />
              <span className="rg-h-left" />
              {cell.right && <span className="rg-h-right" />}
            </>
          )}
          {cell.kind === "dot" && (
            <>
              {cell.up && <span className="rg-v-top" />}
              {cell.down && <span className="rg-v-bottom" />}
              {cell.right && <span className="rg-h-right" />}
              <span className="rg-dot">
                <StatusDot requirements={requirements} state={change.state} />
              </span>
            </>
          )}
        </span>
      ))}
    </span>
  );
}

// The trunk terminator row: main sits at lane 0, the root's line runs into
// it from above.
export function RailGraphTrunk({ lanes }: { lanes: number }) {
  return (
    <span className="rail-graph" style={{ width: lanes * LANE_W }}>
      <span className="rgc">
        <span className="rg-v-top" />
        <span className="rg-dot">
          <TrunkIcon />
        </span>
      </span>
    </span>
  );
}

export const LANE_W = 22;
