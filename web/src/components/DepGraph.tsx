import { useMemo } from "react";
import { projectsClient } from "../api/client";
import { ProjectType } from "../gen/runko/v1/common_pb";
import { projectTypeLabel } from "../lib/format";
import {
  dependencyClosure,
  dependentClosure,
  layoutDag,
  type DepGraphInput,
} from "../lib/depgraph";
import { useRpc, type RpcState } from "../lib/useRpc";

export interface GraphProject extends DepGraphInput {
  type: ProjectType;
  owners: string[];
}

// One fetch shape for every graph surface: summaries + per-project detail
// (deps only live in ProjectDetail). Works identically on both transports.
export function useGraphProjects(): RpcState<GraphProject[]> {
  return useRpc(async () => {
    const list = await projectsClient.listProjects({});
    return Promise.all(
      list.projects.map(async (p) => {
        const detail = (await projectsClient.getProject({ project: p.name })).project!;
        return {
          name: detail.name,
          deps: detail.dependencies?.declared ?? [],
          consumes: detail.dependencies?.consumes ?? [],
          type: detail.type,
          owners: detail.effectiveOwners,
        };
      }),
    );
  }, "graph-projects");
}

export function GraphLegend() {
  return (
    <>
      <span className="chip graph-legend-deps">dependencies</span>
      <span className="chip graph-legend-consumes">consumes (client)</span>
      <span className="chip graph-legend-rdeps">dependents (affected)</span>
    </>
  );
}

// The declared-dependency DAG (§13.3), rendered as layered SVG. `selected`
// highlights both closures: what it depends on (violet, down) and what
// depends on it (amber, up - what re-tests when it changes). Clicking a
// node calls onSelect; clicking the canvas clears (onSelect(undefined)).
export function DepGraph({
  items,
  selected,
  onSelect,
}: {
  items: GraphProject[];
  selected?: string;
  onSelect: (name: string | undefined) => void;
}) {
  const layout = useMemo(() => layoutDag(items), [items]);
  const highlight = useMemo(() => {
    if (!selected || !items.some((p) => p.name === selected)) return undefined;
    return {
      deps: dependencyClosure(items, selected),
      rdeps: dependentClosure(items, selected),
    };
  }, [items, selected]);

  const nodeClass = (name: string): string => {
    if (!highlight) return "graph-node";
    if (name === selected) return "graph-node sel";
    if (highlight.deps.has(name)) return "graph-node in-deps";
    if (highlight.rdeps.has(name)) return "graph-node in-rdeps";
    return "graph-node dim";
  };

  const edgeClass = (from: string, to: string): string => {
    if (!highlight || !selected) return "graph-edge";
    const depSide = new Set([selected, ...highlight.deps]);
    const rdepSide = new Set([selected, ...highlight.rdeps]);
    if (depSide.has(from) && depSide.has(to)) return "graph-edge in-deps";
    if (rdepSide.has(from) && rdepSide.has(to)) return "graph-edge in-rdeps";
    return "graph-edge dim";
  };

  return (
    <div className="graph-scroll">
      <svg
        width={layout.width}
        height={layout.height}
        viewBox={`0 0 ${layout.width} ${layout.height}`}
        role="img"
        aria-label="project dependency graph"
        onClick={() => onSelect(undefined)}
      >
        <defs>
          <marker
            id="arrow"
            viewBox="0 0 8 8"
            refX="7"
            refY="4"
            markerWidth="7"
            markerHeight="7"
            orient="auto-start-reverse"
          >
            <path d="M0 0.5 L7.5 4 L0 7.5 z" />
          </marker>
        </defs>
        {layout.edges.map((e) => (
          <path
            key={`${e.from}->${e.to}`}
            className={edgeClass(e.from, e.to) + (e.kind === "consumes" ? " consumes" : "")}
            d={`M ${e.x1} ${e.y1} C ${e.x1} ${e.y1 + 40}, ${e.x2} ${e.y2 - 40}, ${e.x2} ${e.y2 - 3}`}
            markerEnd="url(#arrow)"
          />
        ))}
        {layout.nodes.map((n) => {
          const p = items.find((x) => x.name === n.name)!;
          return (
            <g
              key={n.name}
              className={nodeClass(n.name)}
              transform={`translate(${n.x}, ${n.y})`}
              onClick={(ev) => {
                ev.stopPropagation();
                onSelect(n.name === selected ? undefined : n.name);
              }}
            >
              <title>
                {n.name} — {projectTypeLabel(p.type)}
                {p.owners.length > 0 ? ` · ${p.owners.join(", ")}` : ""}
              </title>
              <rect width={n.w} height={n.h} rx="8" />
              <text className="graph-name" x={n.w / 2} y={17}>
                {n.name}
              </text>
              <text className="graph-type" x={n.w / 2} y={32}>
                {projectTypeLabel(p.type)}
              </text>
            </g>
          );
        })}
      </svg>
    </div>
  );
}
