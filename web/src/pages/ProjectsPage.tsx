import { publicBrowse } from "../api/client";
import { useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { projectTypeLabel } from "../lib/format";
import { graphProjects, isRootProject, rootFirst } from "../lib/projects";
import { dependencyClosure, dependentClosure } from "../lib/depgraph";
import { DepGraph, GraphLegend, useGraphProjects } from "../components/DepGraph";
import { EmptyState, ErrorNote, InfoTip, Spinner } from "../components/ui";

export function ProjectsPage() {
  const [query, setQuery] = useState("");
  // Graph selection deep-links as /projects?focus=<name> (project pages
  // and old /graph URLs link here).
  const [searchParams, setSearchParams] = useSearchParams();
  const selected = searchParams.get("focus") ?? undefined;
  const setSelected = (name: string | undefined) =>
    setSearchParams(name ? { focus: name } : {}, { replace: true });

  const { data, error, loading } = useGraphProjects();

  const selectedProject = data?.find((p) => p.name === selected);
  const q = query.toLowerCase();
  // Root first: it owns every path no deeper manifest claims, so it
  // reads as the table's first row rather than one service among many.
  const filtered = data && rootFirst(data.filter((p) => !q || p.name.toLowerCase().includes(q)));
  // What the graph would actually draw (everything but the root).
  const graphable = graphProjects(data ?? []);

  return (
    <div className="page">
      <header className="page-header">
        <div className="page-header-row">
          <h1 className="page-title">Projects</h1>
          {!publicBrowse && (
          <Link className="btn btn-primary" to="/projects/new">
            New project
          </Link>
          )}
        </div>
        <p className="page-sub">
          Everything with a PROJECT.yaml on trunk. Solid arrows are build dependencies; dashed
          arrows are API clients (consumes)
          <InfoTip text="Both edge kinds are declared in PROJECT.yaml and drive affected computation - a dependent re-tests on the provider's every change, a client only when its contract surface changes. Inferred (import-scanned) deps are advisory and never appear here." />
        </p>
      </header>

      {loading && <Spinner />}
      {error && <ErrorNote error={error} />}
      {data && data.length === 0 && <EmptyState>No projects on trunk yet.</EmptyState>}

      {data && data.length > 0 && (
        <>
          {/* The graph half renders only when there is a graph: a freshly
              genesis-seeded org owns just its root project, which is
              never a node, and an empty canvas is a bordered blank box.
              The table below still lists everything. */}
          {graphable.length === 0 && (
            <EmptyState>
              No dependency graph yet — only the root project exists. Projects appear here once
              there is one to draw.
            </EmptyState>
          )}
          {graphable.length > 0 && (
          <div className="graph-toolbar">
            {selectedProject ? (
              <>
                <Link className="chip chip-violet" to={`/projects/${selectedProject.name}`}>
                  {selectedProject.name} ↗
                </Link>
                <span className="chip">{projectTypeLabel(selectedProject.type)}</span>
                <span className="chip graph-legend-deps">
                  depends on {dependencyClosure(data, selectedProject.name).size}
                </span>
                <span className="chip graph-legend-rdeps">
                  re-tests {dependentClosure(data, selectedProject.name).size} when changed
                </span>
                <button className="btn btn-sm" onClick={() => setSelected(undefined)}>
                  Clear
                </button>
              </>
            ) : (
              <>
                <GraphLegend />
                <span className="page-sub">— select a project to trace its closures</span>
              </>
            )}
          </div>
          )}

          {graphable.length > 0 && (
            <section className="card graph-panel">
              <DepGraph items={data} selected={selected} onSelect={setSelected} />
            </section>
          )}

          <div className="toolbar" style={{ marginTop: 20 }}>
            <input
              type="text"
              placeholder="Filter by name or path…"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
            />
          </div>

          {filtered && filtered.length === 0 && <EmptyState>No matching projects.</EmptyState>}
          {filtered && filtered.length > 0 && (
            <section className="card">
              <table className="table">
                <thead>
                  <tr>
                    <th>Project</th>
                    <th>Type</th>
                    <th>Owners</th>
                    <th>Declared deps</th>
                  </tr>
                </thead>
                <tbody>
                  {filtered.map((p) => (
                    <tr key={p.name}>
                      <td>
                        <Link to={`/projects/${p.name}`}>{p.name}</Link>
                        {isRootProject(p) && (
                          <>
                            {" "}
                            <span className="chip" title="The repo-root project: it owns every path no deeper PROJECT.yaml claims (root glue, and the root_invalidation/prose rules), so it is not a peer of the projects below and does not appear in the graph">
                              root
                            </span>
                          </>
                        )}
                      </td>
                      <td>
                        <span className="chip">{projectTypeLabel(p.type)}</span>
                      </td>
                      <td>
                        <span className="chip-row">
                          {p.owners.map((o) => (
                            <span className="chip" key={o}>
                              {o}
                            </span>
                          ))}
                        </span>
                      </td>
                      <td>
                        <span className="chip-row">
                          {p.deps.length === 0 && <span className="chip">none</span>}
                          {p.deps.map((d) => (
                            <Link className="chip" key={d} to={`/projects/${d}`}>
                              {d}
                            </Link>
                          ))}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </section>
          )}
        </>
      )}
    </div>
  );
}
