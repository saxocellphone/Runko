import { useState } from "react";
import { Link } from "react-router-dom";
import { projectsClient } from "../api/client";
import { projectTypeLabel } from "../lib/format";
import { useRpc } from "../lib/useRpc";
import { EmptyState, ErrorNote, Spinner } from "../components/ui";

export function ProjectsPage() {
  const [query, setQuery] = useState("");
  const { data, error, loading } = useRpc(
    () => projectsClient.listProjects({ query }),
    `projects-${query}`,
  );

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Projects</h1>
        <p className="page-sub">Everything with a PROJECT.yaml on trunk.</p>
      </header>

      <div className="toolbar">
        <input
          type="text"
          placeholder="Filter by name or path…"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>

      {loading && <Spinner />}
      {error && <ErrorNote error={error} />}
      {data && data.projects.length === 0 && <EmptyState>No matching projects.</EmptyState>}
      {data && data.projects.length > 0 && (
        <section className="card">
          <table className="table">
            <thead>
              <tr>
                <th>Project</th>
                <th>Type</th>
                <th>Path</th>
                <th>Owners</th>
              </tr>
            </thead>
            <tbody>
              {data.projects.map((p) => (
                <tr key={p.id}>
                  <td>
                    <Link to={`/projects/${p.name}`}>{p.name}</Link>
                  </td>
                  <td>
                    <span className="chip">{projectTypeLabel(p.type)}</span>
                  </td>
                  <td className="mono">{p.path}</td>
                  <td>
                    <span className="chip-row">
                      {p.ownersSummary.map((o) => (
                        <span className="chip" key={o}>
                          {o}
                        </span>
                      ))}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </section>
      )}
    </div>
  );
}
