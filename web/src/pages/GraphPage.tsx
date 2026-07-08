import { useMemo } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { projectsClient } from "../api/client";
import { ProjectType } from "../gen/runko/v1/common_pb";
import { projectTypeLabel } from "../lib/format";
import {
  dependencyClosure,
  dependentClosure,
  layoutDag,
  type DepGraphInput,
} from "../lib/depgraph";
import { useRpc } from "../lib/useRpc";
import { EmptyState, ErrorNote, InfoTip, Spinner } from "../components/ui";

interface GraphProject extends DepGraphInput {
  type: ProjectType;
  owners: string[];
}

// The monorepo's declared-dependency DAG (§13.3). Selection highlights the
// two closures that matter: what the project depends on (violet, down) and
// what depends on it (amber, up - the §13.3 affected direction, i.e. what
// re-tests when it changes).
export function GraphPage() {
  // Selection deep-links as /graph?focus=<project> (the project page's
  // "view in graph" link relies on it).
  const [searchParams, setSearchParams] = useSearchParams();
  const selected = searchParams.get("focus") ?? undefined;
  const setSelected = (name: string | undefined) =>
    setSearchParams(name ? { focus: name } : {}, { replace: true });

  const { data, error, loading } = useRpc(async () => {
    const list = await projectsClient.listProjects({});
    const projects: GraphProject[] = await Promise.all(
      list.projects.map(async (p) => {
        const detail = (await projectsClient.getProject({ project: p.name })).project!;
        return {
          name: detail.name,
          deps: detail.dependencies?.declared ?? [],
          type: detail.type,
          owners: detail.effectiveOwners,
        };
      }),
    );
    return projects;
  }, "graph");

  const layout = useMemo(() => (data ? layoutDag(data) : undefined), [data]);
  const highlight = useMemo(() => {
    if (!data || !selected) return undefined;
    return {
      deps: dependencyClosure(data, selected),
      rdeps: dependentClosure(data, selected),
    };
  }, [data, selected]);

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

  const selectedProject = data?.find((p) => p.name === selected);

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">
          Dependency graph
          <InfoTip text="Declared dependencies only - the edges that actually gate merges and drive affected computation. Inferred (import-scanned) deps are advisory and never appear here." />
        </h1>
        <p className="page-sub">
          Arrows point at what a project depends on; foundations sit at the bottom. Click a
          project to trace its closures.
        </p>
      </header>

      {loading && <Spinner />}
      {error && <ErrorNote error={error} />}
      {data && data.length === 0 && <EmptyState>No projects on trunk yet.</EmptyState>}

      {data && layout && data.length > 0 && (
        <>
          <div className="graph-toolbar">
            {selectedProject ? (
              <>
                <Link className="chip chip-violet" to={`/projects/${selectedProject.name}`}>
                  {selectedProject.name} ↗
                </Link>
                <span className="chip">{projectTypeLabel(selectedProject.type)}</span>
                <span className="chip graph-legend-deps">
                  depends on {highlight?.deps.size ?? 0}
                </span>
                <span className="chip graph-legend-rdeps">
                  re-tests {highlight?.rdeps.size ?? 0} when changed
                </span>
                <button className="btn btn-sm" onClick={() => setSelected(undefined)}>
                  Clear
                </button>
              </>
            ) : (
              <>
                <span className="chip graph-legend-deps">dependencies</span>
                <span className="chip graph-legend-rdeps">dependents (affected)</span>
                <span className="page-sub">— select a project</span>
              </>
            )}
          </div>

          <section className="card graph-panel">
            <div className="graph-scroll">
              <svg
                width={layout.width}
                height={layout.height}
                viewBox={`0 0 ${layout.width} ${layout.height}`}
                role="img"
                aria-label="project dependency graph"
                onClick={() => setSelected(undefined)}
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
                    className={edgeClass(e.from, e.to)}
                    d={`M ${e.x1} ${e.y1} C ${e.x1} ${e.y1 + 40}, ${e.x2} ${e.y2 - 40}, ${e.x2} ${e.y2 - 3}`}
                    markerEnd="url(#arrow)"
                  />
                ))}
                {layout.nodes.map((n) => {
                  const p = data.find((x) => x.name === n.name)!;
                  return (
                    <g
                      key={n.name}
                      className={nodeClass(n.name)}
                      transform={`translate(${n.x}, ${n.y})`}
                      onClick={(ev) => {
                        ev.stopPropagation();
                        setSelected(n.name === selected ? undefined : n.name);
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
          </section>
        </>
      )}
    </div>
  );
}
