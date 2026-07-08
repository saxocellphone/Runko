import { Link, useNavigate, useParams } from "react-router-dom";
import { projectsClient } from "../api/client";
import { projectTypeLabel } from "../lib/format";
import { Visibility } from "../gen/runko/v1/common_pb";
import { dependencyClosure, dependentClosure } from "../lib/depgraph";
import { useRpc } from "../lib/useRpc";
import { DepGraph, GraphLegend, useGraphProjects } from "../components/DepGraph";
import { BackLink, ErrorNote, InfoTip, Spinner } from "../components/ui";

export function ProjectPage() {
  // Project names contain slashes (commerce/cart), so the route is a splat.
  const params = useParams();
  const name = params["*"] ?? "";
  const { data, error, loading } = useRpc(
    () => projectsClient.getProject({ project: name }),
    `project-${name}`,
  );

  const back = <BackLink to="/projects">Projects</BackLink>;
  if (loading) return <div className="page">{back}<Spinner /></div>;
  if (error) return <div className="page">{back}<ErrorNote error={error} /></div>;
  const p = data?.project;
  if (!p) return null;

  return (
    <div className="page">
      {back}
      <header className="page-header">
        <h1 className="page-title">{p.name}</h1>
        <p className="page-sub chip-row">
          <span className="chip">{projectTypeLabel(p.type)}</span>
        </p>
      </header>

      <section className="card">
        <dl className="kv">
          <dt>Path</dt>
          <dd className="mono">{p.path}</dd>

          <dt>
            Visibility
            <InfoTip text="Restricted projects are only readable by their owners and org admins. Most projects use the org default." />
          </dt>
          <dd>{p.visibility === Visibility.RESTRICTED ? "restricted" : "default"}</dd>

          <dt>
            Owners
            <InfoTip text="Path owners for this project, resolved from PROJECT.yaml, the nearest OWNERS file, or the org default (in that order). A Change touching this project's paths needs their approval to land." />
          </dt>
          <dd className="chip-row">
            {p.effectiveOwners.length === 0 && <span className="chip">none</span>}
            {p.effectiveOwners.map((o) => (
              <span className="chip" key={o}>
                {o}
              </span>
            ))}
          </dd>

          <dt>
            Capabilities
            <InfoTip text="Opt-in features a project can turn on (rpc, http, deploy, build, ...). Each one generates config on demand - a project with none is still a complete, valid project." />
          </dt>
          <dd className="chip-row">
            {p.capabilities.length === 0 && <span className="chip">none (L0 project)</span>}
            {p.capabilities.map((c) => (
              <span className="chip chip-violet" key={c}>
                {c}
              </span>
            ))}
          </dd>

          <dt>
            Declared deps
            <InfoTip text="Dependencies this project explicitly lists in its own PROJECT.yaml. These are facts, not guesses - they're what determines which projects re-test when this one changes." />
          </dt>
          <dd className="chip-row">
            {(p.dependencies?.declared.length ?? 0) === 0 && <span className="chip">none</span>}
            {p.dependencies?.declared.map((d) => (
              <Link className="chip" key={d} to={`/projects/${d}`}>
                {d}
              </Link>
            ))}
          </dd>

          <dt>
            Inferred deps
            <InfoTip text="Dependencies guessed by scanning imports, shown as suggestions to promote to declared. Advisory only - unlike declared deps, these never block a merge or trigger a re-run." />
          </dt>
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

      <RelatedProjects name={p.name} />
    </div>
  );
}

// The project's dependency neighborhood: itself, everything it
// (transitively) depends on, and everything that depends on it - the two
// closures that matter, cut out of the full monorepo DAG. Clicking a
// neighbor navigates to that project.
function RelatedProjects({ name }: { name: string }) {
  const navigate = useNavigate();
  const { data, error, loading } = useGraphProjects();
  if (loading) return <Spinner />;
  if (error) return <ErrorNote error={error} />;
  if (!data) return null;

  const related = new Set([
    name,
    ...dependencyClosure(data, name),
    ...dependentClosure(data, name),
  ]);
  const items = data
    .filter((p) => related.has(p.name))
    // Drop edges leaving the neighborhood so the layout only draws what
    // it shows.
    .map((p) => ({ ...p, deps: p.deps.filter((d) => related.has(d)) }));

  return (
    <section className="related-graph">
      <div className="graph-toolbar">
        <h2 className="side-heading">Related projects</h2>
        <GraphLegend />
      </div>
      {items.length === 1 ? (
        <p className="page-sub">
          No declared relationships — nothing depends on this project and it depends on
          nothing.
        </p>
      ) : (
        <section className="card graph-panel">
          <DepGraph
            items={items}
            selected={name}
            onSelect={(n) => {
              if (n && n !== name) navigate(`/projects/${n}`);
            }}
          />
        </section>
      )}
    </section>
  );
}
