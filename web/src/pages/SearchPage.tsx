import { useState } from "react";
import { searchClient, projectsClient } from "../api/client";
import { useRpc } from "../lib/useRpc";
import { EmptyState, ErrorNote, Spinner } from "../components/ui";

export function SearchPage() {
  const [input, setInput] = useState("");
  const [query, setQuery] = useState("");
  const [project, setProject] = useState("");

  const projects = useRpc(() => projectsClient.listProjects({}), "search-projects");
  const results = useRpc(
    async () => (query ? searchClient.searchCode({ query, project }) : undefined),
    `search-${query}-${project}`,
  );

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Code search</h1>
        <p className="page-sub">Zoekt-backed trunk search, scoped by project.</p>
      </header>

      <form
        className="toolbar"
        onSubmit={(e) => {
          e.preventDefault();
          setQuery(input);
        }}
      >
        <input
          type="text"
          placeholder="Search trunk…"
          value={input}
          onChange={(e) => setInput(e.target.value)}
        />
        <select value={project} onChange={(e) => setProject(e.target.value)}>
          <option value="">All projects</option>
          {projects.data?.projects.map((p) => (
            <option key={p.id} value={p.name}>
              {p.name}
            </option>
          ))}
        </select>
        <button className="btn" type="submit">
          Search
        </button>
      </form>

      {results.loading && query && <Spinner />}
      {results.error && <ErrorNote error={results.error} />}
      {results.data && results.data.hits.length === 0 && (
        <EmptyState>No hits for “{query}”.</EmptyState>
      )}
      {results.data && results.data.hits.length > 0 && (
        <section className="card">
          {results.data.hits.map((h, i) => (
            <div className="hit" key={`${h.path}:${h.line}:${i}`}>
              <div className="hit-path">
                {h.path}:{h.line}
                {h.projectId && <span className="chip" style={{ marginLeft: 8 }}>{h.projectId}</span>}
              </div>
              <div className="hit-preview">
                <Highlighted text={h.preview} needle={query} />
              </div>
            </div>
          ))}
        </section>
      )}
    </div>
  );
}

function Highlighted({ text, needle }: { text: string; needle: string }) {
  if (!needle) return <>{text}</>;
  const i = text.toLowerCase().indexOf(needle.toLowerCase());
  if (i < 0) return <>{text}</>;
  return (
    <>
      {text.slice(0, i)}
      <mark>{text.slice(i, i + needle.length)}</mark>
      {text.slice(i + needle.length)}
    </>
  );
}
