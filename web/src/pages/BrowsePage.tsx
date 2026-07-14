import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { repoClient } from "../api/client";
import { ChangeState } from "../gen/runko/v1/common_pb";
import {
  TreeEntryType,
  type BlameRegion,
  type CommitInfo,
  type TreeEntry,
} from "../gen/runko/v1/repo_pb";
import { absoluteTime, shortSha, timeAgo } from "../lib/format";
import type { Token } from "../lib/highlight";
import { useRpc } from "../lib/useRpc";
import { EmptyState, ErrorNote, Spinner, StateBadge } from "../components/ui";

// ---- syntax highlighting -------------------------------------------------

// The highlighter (lowlight + registered grammars, lib/highlight.ts) rides
// its own lazy chunk, fetched on first use: the main bundle never carries
// highlight.js, and until the chunk lands - or if it never does - lines
// render plain, exactly the pre-feature view.
type Highlighter = (content: string, path: string) => Token[][] | null;

function useHighlighter(): Highlighter | null {
  const [fn, setFn] = useState<Highlighter | null>(null);
  useEffect(() => {
    let stale = false;
    import("../lib/highlight").then(
      (m) => {
        if (!stale) setFn(() => m.highlightLines);
      },
      () => {}, // plain text is this feature's floor, never an error state
    );
    return () => {
      stale = true;
    };
  }, []);
  return fn;
}

function HighlightedLine({ tokens }: { tokens: Token[] }) {
  return (
    <>
      {tokens.map((t, i) =>
        t.cls ? (
          <span key={i} className={t.cls}>
            {t.text}
          </span>
        ) : (
          t.text
        ),
      )}
    </>
  );
}

// Repo browser (§17.2). Layout re-decided 2026-07-14 from dogfood
// feedback: the TREE is the only directory surface - the old main-pane
// entry listing mirrored it row for row, so it is gone, and project
// boundaries badge in the tree instead (TreeEntry.project exists for
// exactly this - repo.proto). The main column shows what only this page
// can: a directory spends it on the path's HISTORY, full width; a file
// splits it - code (or blame) beside a sticky history rail, so the
// changes that made a file stay in view however long the file is (the
// old below-the-blob history took a full file's worth of scrolling to
// reach, and the collapse toggle that worked around it is gone too).
// Runko's twist stays: history rows and blame regions link to the
// CHANGE that landed the code (§7.4), not a raw commit.
//
// URL state: /browse/<path>, ?view=dir marks directory paths (tree and
// breadcrumbs stamp it), ?view=blame selects the file's blame mode.
// usePaneOpen persists a pane's open/collapsed state per browser.
function usePaneOpen(key: string): [boolean, () => void] {
  const [open, setOpen] = useState(() => window.localStorage.getItem(key) !== "0");
  const toggle = () =>
    setOpen((o) => {
      window.localStorage.setItem(key, o ? "0" : "1");
      return !o;
    });
  return [open, toggle];
}

export function BrowsePage() {
  const params = useParams();
  const [search] = useSearchParams();
  const selected = params["*"] ?? "";
  const view = search.get("view") ?? "";
  const isDir = selected === "" || view === "dir";
  const [treeOpen, toggleTree] = usePaneOpen("runko-browse-tree");

  return (
    <div className="page page-wide">
      <header className="page-header">
        <h1 className="page-title">Browse</h1>
        <p className="page-sub">
          The monorepo at trunk tip. Every path carries the changes that made it.
        </p>
      </header>
      <div className={`browse-layout${treeOpen ? "" : " tree-collapsed"}`}>
        {treeOpen ? (
          <section className="card tree-panel">
            <div className="tree-head">
              <span>Files</span>
              <button
                className="pane-toggle"
                aria-label="Hide file tree"
                title="Hide file tree"
                onClick={toggleTree}
              >
                <ChevronIcon dir="left" />
              </button>
            </div>
            <TreeLevel path="" depth={0} selected={selected} />
          </section>
        ) : (
          <button
            className="card tree-rail"
            aria-label="Show file tree"
            title="Show file tree"
            onClick={toggleTree}
          >
            <ChevronIcon dir="right" />
            <span className="tree-rail-label">Files</span>
          </button>
        )}
        <div className="browse-main">
          <Breadcrumbs path={selected} isDir={isDir} />
          {isDir ? (
            <section className="card history-panel">
              <HistoryHead scope={selected === "" ? "whole repo" : `${selected}/`} />
              <HistoryList path={selected} />
            </section>
          ) : (
            <div className="file-cols">
              <section className="card content-panel">
                <header className="content-head">
                  <span className="file-path" title={selected}>
                    {selected.split("/").pop() ?? selected}
                  </span>
                  <span className="spacer" />
                  <FileModeToggle path={selected} blame={view === "blame"} />
                </header>
                {view === "blame" ? <BlameView path={selected} /> : <CodeView path={selected} />}
              </section>
              <aside className="card history-panel history-rail">
                <HistoryHead scope={null} />
                <HistoryList path={selected} />
              </aside>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}

function HistoryHead({ scope }: { scope: string | null }) {
  return (
    <header className="history-head">
      <HistoryIcon />
      <span>
        History
        {scope && <span className="history-scope"> — {scope}</span>}
      </span>
    </header>
  );
}

// FileModeToggle is the Code/Blame segmented control on the file header.
function FileModeToggle({ path, blame }: { path: string; blame: boolean }) {
  const navigate = useNavigate();
  const setMode = (m: "code" | "blame") =>
    navigate(`/browse/${path}${m === "blame" ? "?view=blame" : ""}`, { replace: true });
  return (
    <div className="seg" role="tablist" aria-label="File view">
      {(["code", "blame"] as const).map((m) => (
        <button
          key={m}
          role="tab"
          aria-selected={blame === (m === "blame")}
          className={`seg-btn${blame === (m === "blame") ? " active" : ""}`}
          onClick={() => setMode(m)}
        >
          {m === "code" ? "Code" : "Blame"}
        </button>
      ))}
    </div>
  );
}

function ChevronIcon({ dir }: { dir: "left" | "right" }) {
  return (
    <svg
      width="13"
      height="13"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      style={{ transform: `rotate(${dir === "left" ? 90 : -90}deg)` }}
      aria-hidden
    >
      <path d="M4 6l4 4 4-4" />
    </svg>
  );
}

// ---- breadcrumbs -----------------------------------------------------

function Breadcrumbs({ path, isDir }: { path: string; isDir: boolean }) {
  const segments = path === "" ? [] : path.split("/");
  return (
    <nav className="crumbs" aria-label="Path">
      <Link className="crumb" to="/browse">
        repo
      </Link>
      {segments.map((seg, i) => {
        const p = segments.slice(0, i + 1).join("/");
        const last = i === segments.length - 1;
        return (
          <span key={p} className="crumb-group">
            <span className="crumb-sep">/</span>
            {last ? (
              <span className="crumb current">
                {seg}
                {isDir ? "/" : ""}
              </span>
            ) : (
              <Link className="crumb" to={`/browse/${p}?view=dir`}>
                {seg}
              </Link>
            )}
          </span>
        );
      })}
    </nav>
  );
}

// ---- tree ------------------------------------------------------------

function TreeLevel({
  path,
  depth,
  selected,
  parentProject,
}: {
  path: string;
  depth: number;
  selected: string;
  parentProject?: string;
}) {
  const { data, error, loading } = useRpc(() => repoClient.getTree({ path }), `tree-${path}`);
  if (loading) return <div className="tree-loading" style={indent(depth)}>…</div>;
  if (error) return <ErrorNote error={error} />;
  // The level's own project: inherited from the DirRow that opened it;
  // the root level infers it from a direct file entry (a file always
  // carries its directory's owner - only a directory can start a
  // project). Dir entries owned by a DIFFERENT project are boundaries
  // and get badged.
  const own =
    parentProject ??
    data?.entries.find((e) => e.type === TreeEntryType.FILE && e.project)?.project ??
    "";
  return (
    <div role={depth === 0 ? "tree" : "group"}>
      {data?.entries.map((e) =>
        e.type === TreeEntryType.DIR ? (
          <DirRow key={e.path} entry={e} depth={depth} selected={selected} levelProject={own} />
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
  levelProject,
}: {
  entry: TreeEntry;
  depth: number;
  selected: string;
  levelProject: string;
}) {
  const navigate = useNavigate();
  // Auto-open ancestors of a deep-linked selection.
  const [open, setOpen] = useState(() => selected.startsWith(entry.path + "/"));
  const current = selected === entry.path;
  // A project boundary always fills the folder icon; the text badge only
  // appears when the project's name doesn't already read off the dir name
  // (a root full of `platform [platform]` rows is the duplication this
  // layout exists to kill).
  const boundary = entry.project !== "" && entry.project !== levelProject;
  return (
    <>
      <button
        className={`tree-row${current ? " selected" : ""}`}
        style={indent(depth)}
        onClick={() => {
          setOpen(current ? !open : true);
          navigate(`/browse/${entry.path}?view=dir`);
        }}
        aria-expanded={open}
        aria-current={current || undefined}
        title={boundary ? `project ${entry.project}` : undefined}
      >
        <span className={`tree-caret${open ? " open" : ""}`}>▸</span>
        <FolderIcon filled={boundary} />
        <span className="tree-name">{entry.name}</span>
        {boundary && entry.project !== entry.name && (
          <span className="tree-project">{entry.project}</span>
        )}
      </button>
      {open && (
        <TreeLevel
          path={entry.path}
          depth={depth + 1}
          selected={selected}
          parentProject={entry.project}
        />
      )}
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

// ---- file content ------------------------------------------------------

function CodeView({ path }: { path: string }) {
  const { data, error, loading } = useRpc(() => repoClient.getBlob({ path }), `blob-${path}`);
  const highlight = useHighlighter();
  const highlighted = useMemo(
    () => (data && !data.binary && highlight ? highlight(data.content, path) : null),
    [data, highlight, path],
  );
  if (loading) return <Spinner />;
  if (error) return <ErrorNote error={error} />;
  if (!data) return null;
  const lines = data.content.split("\n");
  return (
    <div>
      <div className="file-meta-row">
        {data.project && (
          <Link className="chip" to={`/projects/${data.project}`}>
            {data.project}
          </Link>
        )}
        <span className="chip mono" title={`at revision ${data.rev}`}>
          {shortSha(data.rev)}
        </span>
        <span className="chip">{formatSize(data.size)}</span>
      </div>
      {data.binary ? (
        <div className="binary-note">Binary file not shown</div>
      ) : (
        <table className="blob-table">
          <tbody>
            {lines.map((line, i) => (
              <tr key={i}>
                <td className="gutter">{i + 1}</td>
                <td className="line-content">
                  {highlighted?.[i] ? <HighlightedLine tokens={highlighted[i]} /> : line}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {data.truncated && <div className="binary-note">Truncated — fetch via git for the full file.</div>}
    </div>
  );
}

// ---- blame -----------------------------------------------------------

function BlameView({ path }: { path: string }) {
  const { data, error, loading } = useRpc(() => repoClient.blameFile({ path }), `blame-${path}`);
  const highlight = useHighlighter();
  const highlighted = useMemo(
    () => (data && !data.binary && highlight ? highlight(data.lines.join("\n"), path) : null),
    [data, highlight, path],
  );
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
              <td className="line-content">
                {highlighted?.[n - 1] ? <HighlightedLine tokens={highlighted[n - 1]} /> : line}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {data.truncated && <div className="binary-note">Blame truncated — very large file.</div>}
    </div>
  );
}

// ---- history ----------------------------------------------------------

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
            {/* History shows when the commit ENTERED TRUNK - the Change's
                server-side landing time, falling back to committer time
                for pre-Runko/imported commits. Author time reads backwards
                along a rebase-landed trunk (a change authored early and
                landed late shows older than commits beneath it), so it
                lives in the tooltip, not the byline. */}
            <div
              className="history-byline"
              title={`authored ${absoluteTime(c.authoredAt)}${c.landedAt > 0n ? `, landed ${absoluteTime(c.landedAt)}` : ""}`}
            >
              {c.authorName} · {c.landedAt > 0n ? "landed " : ""}
              {timeAgo(c.landedAt > 0n ? c.landedAt : c.committedAt)}
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

function FolderIcon({ filled = false }: { filled?: boolean }) {
  return (
    <svg {...treeIconProps} className={`tree-icon folder${filled ? " project-root" : ""}`} aria-hidden>
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

function HistoryIcon() {
  return (
    <svg {...treeIconProps} aria-hidden>
      <circle cx="8" cy="8" r="6" />
      <path d="M8 4.5V8l2.3 1.6" />
    </svg>
  );
}
