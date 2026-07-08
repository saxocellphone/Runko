import type { ChangeSummary } from "../gen/runko/v1/common_pb";

// Client-side mirror of GetChangeStack's derived relation
// (proto/runko/v1/changes.proto): Change B is stacked on Change A iff
// B.baseSha == A.headSha. Stacks can FORK - two changes built on one base,
// e.g. from a workspace's parallel branches (§12.2) - so the model is a
// tree, never a forced line: the rail renders forks as indented siblings.

export interface StackNode {
  change: ChangeSummary;
  children: StackNode[];
}

// One row of a rendered stack: `depth` is the node's distance from the
// trunk-most root - the rail indents by it, which is how a fork reads.
export interface StackRow {
  change: ChangeSummary;
  depth: number;
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

// flattenStack renders a tree top-first the way the rail draws it:
// descendants above ancestors (the visual "upstack is up"), the root
// (trunk-most) last. Child subtrees render in ascending change-number
// order so each parent sits directly below its own line's rows - keeping
// unrelated fork leaves from ending up visually adjacent.
export function flattenStack(root: StackNode): StackRow[] {
  const rows: StackRow[] = [];
  const walk = (n: StackNode, depth: number) => {
    for (const child of [...n.children].sort((a, b) => Number(a.change.number - b.change.number))) {
      walk(child, depth + 1);
    }
    rows.push({ change: n.change, depth });
  };
  walk(root, 0);
  return rows;
}

// stackSize counts a tree's changes (the inbox card header).
export function stackSize(root: StackNode): number {
  return 1 + root.children.reduce((sum, c) => sum + stackSize(c), 0);
}

function maxNumber(n: StackNode): bigint {
  let max = n.change.number;
  for (const c of n.children) {
    const m = maxNumber(c);
    if (m > max) max = m;
  }
  return max;
}
