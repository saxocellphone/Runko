// Pure layered-DAG layout for the project dependency graph (§13.3's
// declared edges). Hand-rolled Sugiyama-lite instead of a graph library:
// monorepo dep graphs at this org size are dozens of nodes, and the three
// steps that matter (longest-path layering, barycenter ordering, straight
// bezier edges) are ~100 lines - a d3/dagre dependency would be the
// heaviest thing in the bundle for the least code saved.
//
// Convention: an edge goes dependent -> dependency ("depends on", arrow at
// the dependency). Foundations (no deps) sit on the bottom layer; the most
// dependent projects (apps) on top - the way people draw platform stacks.

export interface DepGraphInput {
  name: string;
  deps: string[];
  // §13.3.1 server/client edges: providers this project is a contract
  // client of. Layered and highlighted like deps (a client sits above its
  // provider; it re-tests when the provider's contract changes), drawn
  // dashed to keep the two edge kinds distinguishable.
  consumes?: string[];
}

export interface DagNode {
  name: string;
  layer: number;
  x: number; // top-left
  y: number;
  w: number;
  h: number;
}

export interface DagEdge {
  kind: "dep" | "consumes";
  from: string; // dependent / client
  to: string; // dependency / provider
  x1: number; // bottom-center of `from`
  y1: number;
  x2: number; // top-center of `to`
  y2: number;
}

export interface DagLayout {
  nodes: DagNode[];
  edges: DagEdge[];
  width: number;
  height: number;
}

export const NODE_H = 42;
const LAYER_GAP = 96;
const NODE_GAP = 26;
const PAD = 16;

export function nodeWidth(name: string): number {
  return Math.min(280, Math.max(120, 26 + name.length * 7.4));
}

// Longest path to a no-dependency node; unknown deps are ignored, cycles
// (which declared deps should never form, §13.3) are broken by treating
// the back-edge's target as layer 0 rather than recursing forever.
// edgesOf unifies both edge kinds for layering/ordering/closures - the
// KIND only matters for rendering.
function edgesOf(item: DepGraphInput | undefined): string[] {
  if (!item) return [];
  return [...new Set([...item.deps, ...(item.consumes ?? [])])];
}

export function assignLayers(items: DepGraphInput[]): Map<string, number> {
  const byName = new Map(items.map((p) => [p.name, p]));
  const layers = new Map<string, number>();
  const visiting = new Set<string>();

  const layerOf = (name: string): number => {
    const cached = layers.get(name);
    if (cached !== undefined) return cached;
    if (visiting.has(name)) return 0; // cycle guard
    visiting.add(name);
    const item = byName.get(name);
    const deps = edgesOf(item).filter((d) => byName.has(d) && d !== name);
    const layer =
      deps.length === 0 ? 0 : 1 + Math.max(...deps.map((d) => layerOf(d)));
    visiting.delete(name);
    layers.set(name, layer);
    return layer;
  };

  for (const p of items) layerOf(p.name);
  return layers;
}

export function layoutDag(items: DepGraphInput[]): DagLayout {
  const byName = new Map(items.map((p) => [p.name, p]));
  const layers = assignLayers(items);
  const maxLayer = Math.max(0, ...layers.values());

  // Rows of names, bottom layer first, initially alphabetical.
  const rows: string[][] = Array.from({ length: maxLayer + 1 }, () => []);
  for (const p of [...items].sort((a, b) => a.name.localeCompare(b.name))) {
    rows[layers.get(p.name)!]!.push(p.name);
  }

  // Two barycenter sweeps to reduce crossings: order each row by the mean
  // index of its neighbors in the adjacent row (upward pass uses deps,
  // downward pass uses dependents).
  const indexIn = (row: string[]) => new Map(row.map((n, i) => [n, i]));
  for (const pass of ["up", "down"] as const) {
    const order = pass === "up" ? rows.keys() : [...rows.keys()].reverse();
    for (const li of order) {
      const row = rows[li]!;
      const neighborRow =
        pass === "up" ? rows[li - 1] : rows[li + 1];
      if (!neighborRow || neighborRow.length === 0) continue;
      const idx = indexIn(neighborRow);
      const bary = (name: string): number => {
        const neighbors =
          pass === "up"
            ? edgesOf(byName.get(name)).filter((d) => idx.has(d))
            : neighborRow.filter((n) => edgesOf(byName.get(n)).includes(name));
        if (neighbors.length === 0) return Number.MAX_SAFE_INTEGER; // keep at right
        return neighbors.reduce((s, n) => s + idx.get(n)!, 0) / neighbors.length;
      };
      const keyed = row.map((n) => [n, bary(n)] as const);
      keyed.sort((a, b) => a[1] - b[1] || a[0].localeCompare(b[0]));
      rows[li] = keyed.map(([n]) => n);
    }
  }

  // Coordinates: rows are horizontally centered; bottom layer at the
  // bottom (maxLayer - layer flips SVG's y-down axis).
  const rowWidth = (row: string[]) =>
    row.reduce((s, n) => s + nodeWidth(n), 0) + NODE_GAP * Math.max(0, row.length - 1);
  const width = Math.max(...rows.map(rowWidth), 0) + PAD * 2;
  const height = (maxLayer + 1) * NODE_H + maxLayer * LAYER_GAP + PAD * 2;

  const nodes = new Map<string, DagNode>();
  rows.forEach((row, layer) => {
    let x = PAD + (width - PAD * 2 - rowWidth(row)) / 2;
    const y = PAD + (maxLayer - layer) * (NODE_H + LAYER_GAP);
    for (const name of row) {
      const w = nodeWidth(name);
      nodes.set(name, { name, layer, x, y, w, h: NODE_H });
      x += w + NODE_GAP;
    }
  });

  const edges: DagEdge[] = [];
  // Sorted iteration keeps the whole layout a pure function of the SET of
  // inputs, not their order.
  for (const p of [...items].sort((a, b) => a.name.localeCompare(b.name))) {
    const from = nodes.get(p.name)!;
    const push = (d: string, kind: "dep" | "consumes") => {
      const to = nodes.get(d);
      if (!to || d === p.name) return;
      edges.push({
        kind,
        from: p.name,
        to: d,
        x1: from.x + from.w / 2,
        y1: from.y + from.h,
        x2: to.x + to.w / 2,
        y2: to.y,
      });
    };
    for (const d of p.deps) push(d, "dep");
    // A pair with both kinds keeps only the build edge - one arrow per pair.
    for (const d of (p.consumes ?? []).filter((c) => !p.deps.includes(c))) push(d, "consumes");
  }

  return { nodes: [...nodes.values()], edges, width, height };
}

// Transitive closures for selection highlighting: everything `name`
// depends on, and everything that depends on `name` (the §13.3 affected
// direction - "what re-tests when this changes").
export function dependencyClosure(items: DepGraphInput[], name: string): Set<string> {
  const byName = new Map(items.map((p) => [p.name, p]));
  const out = new Set<string>();
  const walk = (n: string) => {
    for (const d of edgesOf(byName.get(n))) {
      if (byName.has(d) && !out.has(d)) {
        out.add(d);
        walk(d);
      }
    }
  };
  walk(name);
  return out;
}

export function dependentClosure(items: DepGraphInput[], name: string): Set<string> {
  const out = new Set<string>();
  let grew = true;
  while (grew) {
    grew = false;
    for (const p of items) {
      if (out.has(p.name) || p.name === name) continue;
      if (edgesOf(p).some((d) => d === name || out.has(d))) {
        out.add(p.name);
        grew = true;
      }
    }
  }
  return out;
}
