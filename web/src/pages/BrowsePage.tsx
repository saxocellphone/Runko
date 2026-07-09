import { useEffect, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { repoClient } from "../api/client";
import { ChangeState } from "../gen/runko/v1/common_pb";
import {
  TreeEntryType,
  type BlameRegion,
  type CommitInfo,
  type TreeEntry,
} from "../gen/runko/v1/repo_pb";
import { shortSha, timeAgo } from "../lib/format";
import { useRpc } from "../lib/useRpc";
import { EmptyState, ErrorNote, Spinner, StateBadge } from "../components/ui";

// Repo browser (§17.2): lazy directory tree on the left; on the right the
// selected file (Code / Blame / History tabs) or a directory's history.
// Gerrit-inspired at the data level, with Runko's twist: history and blame
// rows link to the CHANGE that landed the code (§7.4), not a raw commit.
// Selection deep-links as /browse/<path>; ?view= carries dir-ness and tab.
export function BrowsePage() {
  const params = useParams();
  const [search] = useSearchParams();
  const selected = params["*"] ?? "";
  const view = search.get("view") ?? ""; // "", "dir", "blame", "history"

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Browse</h1>
        <p className="page-sub">
          The monorepo at trunk tip. Click a directory or file to see the changes behind it.
        </p>
      </header>
      <div className="browse-layout">
        <section className="card tree-panel">
          <TreeLevel path="" depth={0} selected={selected} />
        </section>
        <section className="card file-panel">
          {selected === "" ? (
            <HistoryPanel path="" title="Repository history" />
          ) : view === "dir" ? (
            <HistoryPanel path={selected} title={selected + "/"} />
          ) : (
            <BlobView path={selected} tab={view === "blame" || view === "history" ? view : "code"} />
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
  const navigate = useNavigate();
  // Auto-open ancestors of a deep-linked selection.
  const [open, setOpen] = useState(() => selected.startsWith(entry.path + "/"));
  const current = selected === entry.path;
  return (
    <>
      <button
        className={`tree-row${current ? " selected" : ""}`}
        style={indent(depth)}
        onClick={() => {
          // Selecting a directory shows its history; a second click on
          // the already-selected dir just toggles expansion.
          setOpen(current ? !open : true);
          navigate(`/browse/${entry.path}?view=dir`);
        }}
        aria-expanded={open}
        aria-current={current || undefined}
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

// ---- file panel: Code / Blame / History tabs ----

function BlobView({ path, tab }: { path: string; tab: "code" | "blame" | "history" }) {
  const navigate = useNavigate();
  const setTab = (t: string) =>
    navigate(`/browse/${path}${t === "code" ? "" : `?view=${t}`}`, { replace: true });
  return (
    <div>
      <div className="panel-tabs" role="tablist">
        {(["code", "blame", "history"] as const).map((t) => (
          <button
            key={t}
            role="tab"
            aria-selected={tab === t}
            className={`panel-tab${tab === t ? " active" : ""}`}
            onClick={() => setTab(t)}
          >
            {t === "code" ? "Code" : t === "blame" ? "Blame" : "History"}
          </button>
        ))}
      </div>
      {tab === "code" && <CodeView path={path} />}
      {tab === "blame" && <BlameView path={path} />}
      {tab === "history" && <HistoryList path={path} />}
    </div>
  );
}

function CodeView({ path }: { path: string }) {
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

// ---- blame ----

function BlameView({ path }: { path: string }) {
  const { data, error, loading } = useRpc(() => repoClient.blameFile({ path }), `blame-${path}`);
  if (loading) return <Spinner />;
  if (error) return <ErrorNote error={error} />;
  if (!data) return null;
  if (data.binary) return <div className="binary-note">Binary file — nothing to blame.</div>;

  // Age tint: newest regions glow accent, oldest fade out. Buckets over
  // the file's own time range so every file uses the full scale.
  const times = data.regions.map((r) => Number(r.authoredAt));
  const min = Math.min(...times);
  const span = Math.max(1, Math.max(...times) - min);
  const bucket = (r: BlameRegion) => Math.round(((Number(r.authoredAt) - min) / span) * 4);

  // Region lookup per line (regions are ordered + contiguous).
  const rows: { line: string; n: number; region?: BlameRegion }[] = [];
  let ri = 0;
  data.lines.forEach((line, i) => {
    const n = i + 1;
    while (ri < data.regions.length) {
      const r = data.regions[ri]!;
      if (n < r.startLine + r.lineCount) break;
      ri++;
    }
    const r = data.regions[ri];
    rows.push({ line, n, region: r && r.startLine === n ? r : undefined });
  });

  return (
    <div>
      <table className="blob-table blame-table">
        <tbody>
          {rows.map(({ line, n, region }) => (
            <tr key={n} className={region ? "blame-region-start" : undefined}>
              {region && (
                <td className={`blame-meta blame-age-${bucket(region)}`} rowSpan={region.lineCount}>
                  <div className="blame-subject" title={region.subject}>
                    {region.changeId ? (
                      <Link to={`/changes/${region.changeId}`}>{region.subject}</Link>
                    ) : (
                      region.subject
                    )}
                  </div>
                  <div className="blame-byline">
                    {region.authorName} · {timeAgo(region.authoredAt)} ·{" "}
                    <span className="mono">{shortSha(region.sha)}</span>
                  </div>
                </td>
              )}
              <td className="gutter">{n}</td>
              <td className="line-content">{line}</td>
            </tr>
          ))}
        </tbody>
      </table>
      {data.truncated && <div className="binary-note">Blame truncated — very large file.</div>}
    </div>
  );
}

// ---- history ----

function HistoryPanel({ path, title }: { path: string; title: string }) {
  return (
    <div>
      <header className="file-panel-head">
        <span className="file-path">{title}</span>
      </header>
      <HistoryList path={path} />
    </div>
  );
}

function HistoryList({ path }: { path: string }) {
  const [commits, setCommits] = useState<CommitInfo[]>([]);
  const [token, setToken] = useState("");
  const [nextToken, setNextToken] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    setCommits([]);
    setToken("");
    setNextToken(null);
    setLoading(true);
    setError(null);
  }, [path]);

  useEffect(() => {
    let cancelled = false;
    void repoClient
      .listCommits({ path, pageToken: token })
      .then((resp) => {
        if (cancelled) return;
        setCommits((prev) => [...prev, ...resp.commits]);
        setNextToken(resp.nextPageToken || null);
        setLoading(false);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : String(err));
        setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [path, token]);

  if (error) return <div className="binary-note">{error}</div>;
  if (loading && commits.length === 0) return <Spinner />;
  if (commits.length === 0) return <EmptyState>No history for this path yet.</EmptyState>;

  return (
    <div className="history-list">
      {commits.map((c) => (
        <div key={c.sha} className="history-row">
          <div className="history-main">
            {c.changeId ? (
              <Link className="history-subject" to={`/changes/${c.changeId}`}>
                {c.subject}
              </Link>
            ) : (
              <span className="history-subject">{c.subject}</span>
            )}
            <div className="history-byline">
              {c.authorName} · {timeAgo(c.authoredAt)}
            </div>
          </div>
          {c.changeState !== ChangeState.UNSPECIFIED && <StateBadge state={c.changeState} />}
          <span className="chip mono" title={c.sha}>
            {shortSha(c.sha)}
          </span>
        </div>
      ))}
      {nextToken && (
        <button className="btn btn-sm history-more" onClick={() => setToken(nextToken)}>
          Load older
        </button>
      )}
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
