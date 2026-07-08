import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { repoClient } from "../api/client";
import { TreeEntryType, type TreeEntry } from "../gen/runko/v1/repo_pb";
import { shortSha } from "../lib/format";
import { useRpc } from "../lib/useRpc";
import { EmptyState, ErrorNote, Spinner } from "../components/ui";

// Barebones repo browser (§17.2): lazy directory tree on the left, file
// viewer on the right. Selection deep-links as /browse/<path>.
export function BrowsePage() {
  const params = useParams();
  const selected = params["*"] ?? "";

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Browse</h1>
        <p className="page-sub">The monorepo at trunk tip.</p>
      </header>
      <div className="browse-layout">
        <section className="card tree-panel">
          <TreeLevel path="" depth={0} selected={selected} />
        </section>
        <section className="card file-panel">
          {selected ? (
            <BlobView path={selected} />
          ) : (
            <EmptyState>Select a file to view it.</EmptyState>
          )}
        </section>
      </div>
    </div>
  );
}

function TreeLevel({
  path,
  depth,
  selected,
}: {
  path: string;
  depth: number;
  selected: string;
}) {
  const { data, error, loading } = useRpc(() => repoClient.getTree({ path }), `tree-${path}`);
  if (loading) return <div className="tree-loading" style={indent(depth)}>…</div>;
  if (error) return <ErrorNote error={error} />;
  return (
    <div role={depth === 0 ? "tree" : "group"}>
      {data?.entries.map((e) =>
        e.type === TreeEntryType.DIR ? (
          <DirRow key={e.path} entry={e} depth={depth} selected={selected} />
        ) : (
          <FileRow key={e.path} entry={e} depth={depth} selected={selected} />
        ),
      )}
    </div>
  );
}

function DirRow({
  entry,
  depth,
  selected,
}: {
  entry: TreeEntry;
  depth: number;
  selected: string;
}) {
  // Auto-open ancestors of a deep-linked selection.
  const [open, setOpen] = useState(() => selected.startsWith(entry.path + "/"));
  return (
    <>
      <button
        className="tree-row"
        style={indent(depth)}
        onClick={() => setOpen(!open)}
        aria-expanded={open}
      >
        <span className={`tree-caret${open ? " open" : ""}`}>▸</span>
        <FolderIcon />
        <span className="tree-name">{entry.name}</span>
      </button>
      {open && <TreeLevel path={entry.path} depth={depth + 1} selected={selected} />}
    </>
  );
}

function FileRow({
  entry,
  depth,
  selected,
}: {
  entry: TreeEntry;
  depth: number;
  selected: string;
}) {
  const navigate = useNavigate();
  const current = selected === entry.path;
  return (
    <button
      className={`tree-row${current ? " selected" : ""}`}
      style={indent(depth)}
      onClick={() => navigate(`/browse/${entry.path}`)}
      aria-current={current || undefined}
    >
      <span className="tree-caret" />
      <FileIcon />
      <span className="tree-name">{entry.name}</span>
    </button>
  );
}

const indent = (depth: number) => ({ paddingLeft: `${10 + depth * 14}px` });

function BlobView({ path }: { path: string }) {
  const { data, error, loading } = useRpc(() => repoClient.getBlob({ path }), `blob-${path}`);
  if (loading) return <Spinner />;
  if (error) return <ErrorNote error={error} />;
  if (!data) return null;
  const lines = data.content.split("\n");
  return (
    <div>
      <header className="file-panel-head">
        <span className="file-path" title={data.path}>
          {data.path}
        </span>
        <span className="spacer" />
        {data.project && (
          <Link className="chip" to={`/projects/${data.project}`}>
            {data.project}
          </Link>
        )}
        <span className="chip mono" title={`at revision ${data.rev}`}>
          {shortSha(data.rev)}
        </span>
        <span className="chip">{formatSize(data.size)}</span>
      </header>
      {data.binary ? (
        <div className="binary-note">Binary file not shown</div>
      ) : (
        <table className="blob-table">
          <tbody>
            {lines.map((line, i) => (
              <tr key={i}>
                <td className="gutter">{i + 1}</td>
                <td className="line-content">{line}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {data.truncated && <div className="binary-note">Truncated — fetch via git for the full file.</div>}
    </div>
  );
}

function formatSize(size: bigint): string {
  const n = Number(size);
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / 1024 / 1024).toFixed(1)} MiB`;
}

const treeIconProps = {
  width: 14,
  height: 14,
  viewBox: "0 0 16 16",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.3,
  strokeLinecap: "round",
  strokeLinejoin: "round",
} as const;

function FolderIcon() {
  return (
    <svg {...treeIconProps} className="tree-icon folder" aria-hidden>
      <path d="M2 4.5c0-.8.7-1.5 1.5-1.5h3l1.5 2h4.5c.8 0 1.5.7 1.5 1.5v6c0 .8-.7 1.5-1.5 1.5h-9C2.7 14 2 13.3 2 12.5v-8z" />
    </svg>
  );
}

function FileIcon() {
  return (
    <svg {...treeIconProps} className="tree-icon" aria-hidden>
      <path d="M4 2h5l3 3v9a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1z" />
      <path d="M9 2v3h3" />
    </svg>
  );
}
