import type { ChangeSummary } from "../gen/runko/v1/common_pb";

// Client-side mirror of GetChangeStack's derived relation
// (proto/runko/v1/changes.proto): Change B is stacked on Change A iff
// B.baseSha == A.headSha. Stacks can FORK - two changes built on one base,
// e.g. from a workspace's parallel branches (§12.2) - so the model is a
// tree, and the rail renders it git-log-graph style: fixed-width lanes
// with real pass-through lines and merge corners, so connectors actually
// line up across rows.

export interface StackNode {
  change: ChangeSummary;
  children: StackNode[];
}

// buildStackForest groups changes into connected trees (roots are the
// trunk-based changes), newest activity first. Used by the inbox on the
// full open list, and by the change page on GetChangeStack's flat tree.
export function buildStackForest(changes: ChangeSummary[]): StackNode[] {
  const byHead = new Map<string, ChangeSummary>();
  for (const c of changes) byHead.set(c.headSha, c);

  const nodes = new Map<string, StackNode>();
  const node = (c: ChangeSummary): StackNode => {
    let n = nodes.get(c.id);
    if (!n) {
      n = { change: c, children: [] };
      nodes.set(c.id, n);
    }
    return n;
  };

  const roots: StackNode[] = [];
  for (const c of changes) {
    const parent = byHead.get(c.baseSha);
    if (parent && parent.id !== c.id) {
      node(parent).children.push(node(c));
    } else {
      roots.push(node(c));
    }
  }
  for (const n of nodes.values()) {
    n.children.sort((a, b) => Number(a.change.number - b.change.number));
  }
  roots.sort((a, b) => Number(maxNumber(b) - maxNumber(a)));
  return roots;
}

// stackSize counts a tree's changes (the inbox card header).
export function stackSize(root: StackNode): number {
  return 1 + root.children.reduce((sum, c) => sum + stackSize(c), 0);
}

export function stackHasFork(root: StackNode): boolean {
  return root.children.length > 1 || root.children.some(stackHasFork);
}

function maxNumber(n: StackNode): bigint {
  let max = n.change.number;
  for (const c of n.children) {
    const m = maxNumber(c);
    if (m > max) max = m;
  }
  return max;
}

// ---------------------------------------------------------------- layout

export interface StackLayoutRow {
  change: ChangeSummary;
  lane: number;
}

// One child->parent edge, in row/lane coordinates (parent is always on a
// LATER row - rows render top-first, descendants above ancestors).
interface railEdge {
  fromRow: number;
  fromLane: number;
  toRow: number;
  toLane: number;
}

export interface StackLayout {
  rows: StackLayoutRow[];
  lanes: number;
  edges: railEdge[];
}

// layoutStack flattens a tree into rows + lanes:
//  - rows: every subtree contiguous, node after (below) its children,
//    child subtrees in ascending change-number order - so the last child
//    sits directly above its parent and continues the parent's lane
//    straight; earlier siblings get lanes to the right and merge into the
//    parent row with a corner.
//  - lane math: lane(last child) = lane(parent); each earlier child is
//    offset by the total width of the subtrees between it and the parent,
//    which is what makes crossings impossible in a tree.
export function layoutStack(root: StackNode): StackLayout {
  const width = (n: StackNode): number =>
    n.children.length === 0 ? 1 : n.children.reduce((s, c) => s + width(c), 0);

  const rows: StackLayoutRow[] = [];
  const edges: railEdge[] = [];
  let lanes = 1;

  // First pass: assign lanes; second: emit rows in render order and
  // resolve edge row indices.
  const laneOf = new Map<StackNode, number>();
  const assign = (n: StackNode, lane: number) => {
    laneOf.set(n, lane);
    lanes = Math.max(lanes, lane + 1);
    let offset = lane;
    // children render top-to-bottom in ascending order; the LAST one is
    // directly above the parent and inherits its lane. Walk from the last
    // child leftward in render order, pushing earlier ones further right.
    const kids = n.children;
    for (let i = kids.length - 1; i >= 0; i--) {
      assign(kids[i]!, offset);
      offset += width(kids[i]!);
    }
  };
  assign(root, 0);

  const rowOf = new Map<StackNode, number>();
  const emit = (n: StackNode) => {
    for (const c of n.children) emit(c);
    rowOf.set(n, rows.length);
    rows.push({ change: n.change, lane: laneOf.get(n)! });
  };
  emit(root);

  const collect = (n: StackNode) => {
    for (const c of n.children) {
      edges.push({
        fromRow: rowOf.get(c)!,
        fromLane: laneOf.get(c)!,
        toRow: rowOf.get(n)!,
        toLane: laneOf.get(n)!,
      });
      collect(c);
    }
  };
  collect(root);

  return { rows, lanes, edges };
}

// ------------------------------------------------------------- rail cells

export type RailCell =
  | { kind: "empty" }
  | { kind: "v" } // an outer line passing through this row
  | { kind: "h" } // merge horizontal passing through toward the parent
  | { kind: "corner"; right: boolean } // a fork line turning left into its parent
  | { kind: "dot"; up: boolean; down: boolean; right: boolean };

// railCells computes what to draw in each lane of one row - pure, so the
// geometry is unit-testable without a browser.
export function railCells(layout: StackLayout, rowIndex: number): RailCell[] {
  const row = layout.rows[rowIndex]!;
  const cornerLanes = layout.edges
    .filter((e) => e.toRow === rowIndex && e.fromLane !== e.toLane)
    .map((e) => e.fromLane);
  const maxCorner = Math.max(-1, ...cornerLanes);

  const cells: RailCell[] = [];
  for (let lane = 0; lane < layout.lanes; lane++) {
    if (lane === row.lane) {
      cells.push({
        kind: "dot",
        // A straight child directly above continues this lane's line.
        up: layout.edges.some((e) => e.toRow === rowIndex && e.fromLane === lane),
        // Every change connects downward - to its parent or to trunk.
        down: true,
        right: maxCorner > lane,
      });
    } else if (cornerLanes.includes(lane)) {
      cells.push({ kind: "corner", right: lane < maxCorner });
    } else if (lane > row.lane && lane < maxCorner) {
      cells.push({ kind: "h" });
    } else if (
      layout.edges.some((e) => e.fromLane === lane && e.fromRow < rowIndex && e.toRow > rowIndex)
    ) {
      cells.push({ kind: "v" });
    } else {
      cells.push({ kind: "empty" });
    }
  }
  return cells;
}

// ---- §12.2 provenance: workspace branch ↔ stack ----

// stackOrigin returns the workspace-branch provenance a stack was pushed
// from: the first origin found walking from the root (base-most change)
// up. Changes in one stack normally share a workspace (they were pushed
// from its worktrees); branches may differ when the stack forks - per-row
// branch labels handle that, this names the stack's home workspace.
export function stackOrigin(
  root: StackNode,
): { workspace: string; branch: string } | undefined {
  const queue: StackNode[] = [root];
  while (queue.length > 0) {
    const n = queue.shift()!;
    if (n.change.originWorkspace) {
      return { workspace: n.change.originWorkspace, branch: n.change.originBranch };
    }
    queue.push(...n.children);
  }
  return undefined;
}

// changesByOrigin groups changes by their workspace branch - the
// workspaces page uses it to show each branch's in-flight stack next to
// the branch itself. Keyed "<workspace>/<branch>" (both are single path
// segments by construction, so "/" cannot collide); changes with no
// provenance are omitted. Within a group, base-most change first (by
// number when present, else stable input order).
export function changesByOrigin(changes: ChangeSummary[]): Map<string, ChangeSummary[]> {
  const groups = new Map<string, ChangeSummary[]>();
  for (const c of changes) {
    if (!c.originWorkspace) continue;
    const key = `${c.originWorkspace}/${c.originBranch}`;
    const group = groups.get(key) ?? [];
    group.push(c);
    groups.set(key, group);
  }
  for (const group of groups.values()) {
    group.sort((a, b) => (a.number < b.number ? -1 : a.number > b.number ? 1 : 0));
  }
  return groups;
}
