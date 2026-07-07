import { Link, useParams } from "react-router-dom";
import { projectsClient } from "../api/client";
import { projectTypeLabel } from "../lib/format";
import { Visibility } from "../gen/runko/v1/common_pb";
import { useRpc } from "../lib/useRpc";
import { ErrorNote, Spinner } from "../components/ui";

export function ProjectPage() {
  // Project names contain slashes (commerce/cart), so the route is a splat.
  const params = useParams();
  const name = params["*"] ?? "";
  const { data, error, loading } = useRpc(
    () => projectsClient.getProject({ project: name }),
    `project-${name}`,
  );

  if (loading) return <div className="page"><Spinner /></div>;
  if (error) return <div className="page"><ErrorNote error={error} /></div>;
  const p = data?.project;
  if (!p) return null;

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">{p.name}</h1>
        <p className="page-sub">
          <span className="chip">{projectTypeLabel(p.type)}</span>
        </p>
      </header>

      <section className="card">
        <dl className="kv">
          <dt>Path</dt>
          <dd className="mono">{p.path}</dd>

          <dt>Visibility</dt>
          <dd>{p.visibility === Visibility.RESTRICTED ? "restricted" : "default"}</dd>

          <dt>Owners</dt>
          <dd className="chip-row">
            {p.effectiveOwners.length === 0 && <span className="chip">none</span>}
            {p.effectiveOwners.map((o) => (
              <span className="chip" key={o}>
                {o}
              </span>
            ))}
          </dd>

          <dt>Capabilities</dt>
          <dd className="chip-row">
            {p.capabilities.length === 0 && <span className="chip">none (L0 project)</span>}
            {p.capabilities.map((c) => (
              <span className="chip chip-violet" key={c}>
                {c}
              </span>
            ))}
          </dd>

          <dt>Declared deps</dt>
          <dd className="chip-row">
            {(p.dependencies?.declared.length ?? 0) === 0 && <span className="chip">none</span>}
            {p.dependencies?.declared.map((d) => (
              <Link className="chip" key={d} to={`/projects/${d}`}>
                {d}
              </Link>
            ))}
          </dd>

          <dt>Inferred deps</dt>
          <dd className="chip-row">
            {(p.dependencies?.inferred.length ?? 0) === 0 ? (
              <span className="chip">none (advisory-only, never gates)</span>
            ) : (
              p.dependencies?.inferred.map((d) => (
                <span className="chip" key={d}>
                  {d}
                </span>
              ))
            )}
          </dd>
        </dl>
      </section>
    </div>
  );
}
