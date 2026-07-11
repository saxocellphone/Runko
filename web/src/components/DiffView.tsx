import { useState } from "react";
import { Link } from "react-router-dom";
import {
  DiffLineType,
  FileDiffStatus,
  type DiffHunk,
  type FileDiff,
} from "../gen/runko/v1/changes_pb";
import { CommentSide } from "../gen/runko/v1/common_pb";
import { publicBrowse } from "../api/client";
import { lineKey, type Thread } from "../lib/comments";
import { diffLineCount, initialFolds, isLargeDiff, threadPathSet } from "../lib/difffold";
import { CommentComposer, ThreadCard, type ReviewActions } from "./ReviewThreads";

// Review conversation anchoring (§13.4.1, stage 16b): current-head
// line-level threads render inline under their diff row, file-level
// threads at the top of the file card; a hover "+" on any numbered line
// opens a composer anchored there. Absent (e.g. the repo browser reusing
// this view someday), the diff renders exactly as before.
export interface DiffReview {
  byLine: Map<string, Thread[]>;
  byFile: Map<string, Thread[]>;
  actions: ReviewActions;
  busy: boolean;
}

export function DiffView({ files, review }: { files: FileDiff[]; review?: DiffReview }) {
  const additions = files.reduce((n, f) => n + f.additions, 0);
  const deletions = files.reduce((n, f) => n + f.deletions, 0);
  // Fold state lives here so expand/collapse-all works (difffold.ts owns
  // the policy): oversized files and many-file changes start collapsed;
  // files carrying review threads never do. Collapsed content is not
  // mounted at all - the point is rendering cost, not just screen space.
  const [folds, setFolds] = useState<Record<string, boolean>>(() =>
    initialFolds(files, review ? threadPathSet(review.byLine, review.byFile) : new Set()),
  );
  const setAll = (collapsed: boolean) =>
    setFolds(Object.fromEntries(files.map((f) => [f.path, collapsed])));
  const anyExpanded = files.some((f) => !folds[f.path]);
  return (
    <div>
      <div className="diff-summary">
        <span>
          {files.length} file{files.length === 1 ? "" : "s"} changed
        </span>
        <span className="added-count">+{additions}</span>
        <span className="deleted-count">−{deletions}</span>
        <span className="spacer" />
        {files.length > 1 &&
          (anyExpanded ? (
            <button className="btn btn-sm" onClick={() => setAll(true)}>
              Collapse all
            </button>
          ) : (
            <button className="btn btn-sm" onClick={() => setAll(false)}>
              Expand all
            </button>
          ))}
      </div>
      {files.map((f) => (
        <FileDiffCard
          key={f.path}
          file={f}
          review={review}
          collapsed={!!folds[f.path]}
          onToggle={() => setFolds((prev) => ({ ...prev, [f.path]: !prev[f.path] }))}
        />
      ))}
    </div>
  );
}

const statusChip: Record<number, { label: string; cls: string } | undefined> = {
  [FileDiffStatus.ADDED]: { label: "added", cls: "chip-green" },
  [FileDiffStatus.DELETED]: { label: "deleted", cls: "chip-red" },
  [FileDiffStatus.RENAMED]: { label: "renamed", cls: "chip-violet" },
};

function FileDiffCard({
  file,
  review,
  collapsed,
  onToggle,
}: {
  file: FileDiff;
  review?: DiffReview;
  collapsed: boolean;
  onToggle: () => void;
}) {
  // The one line-level composer open in this file, keyed like the threads.
  const [composerAt, setComposerAt] = useState<{ side: CommentSide; line: number } | null>(null);
  const chip = statusChip[file.status];
  const fileThreads = review?.byFile.get(file.path) ?? [];
  return (
    <section className={`card file-diff${collapsed ? " collapsed" : ""}`}>
      <header className="file-head" onClick={onToggle}>
        <span className="chevron">{collapsed ? "▶" : "▼"}</span>
        <span className="file-path">
          {file.oldPath && <span className="file-old-path">{file.oldPath}</span>}
          {file.status === FileDiffStatus.DELETED ? (
            file.path
          ) : (
            <Link
              className="path-link"
              to={`/browse/${file.path}`}
              title="open in the repo browser"
              onClick={(e) => e.stopPropagation()}
            >
              {file.path}
            </Link>
          )}
        </span>
        {chip && <span className={`chip ${chip.cls}`}>{chip.label}</span>}
        {isLargeDiff(file) && (
          <span
            className="chip"
            title={`${diffLineCount(file)} diff lines - large diffs start collapsed; click the header to expand`}
          >
            large diff
          </span>
        )}
        <span className="spacer" />
        {file.project && (
          <Link
            className="chip"
            to={`/projects/${file.project}`}
            onClick={(e) => e.stopPropagation()}
          >
            {file.project}
          </Link>
        )}
        <span className="added-count">+{file.additions}</span>
        <span className="deleted-count">−{file.deletions}</span>
      </header>
      {!collapsed && fileThreads.length > 0 && review && (
        <div className="file-threads">
          {fileThreads.map((t) => (
            <ThreadCard key={t.root.id} thread={t} actions={review.actions} busy={review.busy} />
          ))}
        </div>
      )}
      {!collapsed &&
        (file.binary ? (
          <div className="binary-note">Binary file not shown</div>
        ) : (
          <table className="hunk-table">
            <tbody>
              {file.hunks.map((h, i) => (
                <HunkRows
                  key={i}
                  hunk={h}
                  path={file.path}
                  review={review}
                  composerAt={composerAt}
                  setComposerAt={setComposerAt}
                />
              ))}
            </tbody>
          </table>
        ))}
    </section>
  );
}

/** The anchor a diff line takes when commented on: the change's version of
 * the file (head side) when the line exists there, the base version for
 * removed lines - matching the CLI's --side semantics. */
function lineAnchor(line: { oldLine: number; newLine: number }): { side: CommentSide; line: number } | null {
  if (line.newLine > 0) return { side: CommentSide.HEAD, line: line.newLine };
  if (line.oldLine > 0) return { side: CommentSide.BASE, line: line.oldLine };
  return null;
}

function HunkRows({
  hunk,
  path,
  review,
  composerAt,
  setComposerAt,
}: {
  hunk: DiffHunk;
  path: string;
  review?: DiffReview;
  composerAt: { side: CommentSide; line: number } | null;
  setComposerAt: (v: { side: CommentSide; line: number } | null) => void;
}) {
  const commentable = review && !publicBrowse;
  return (
    <>
      <tr className="hunk-head">
        <td className="gutter" colSpan={2} />
        <td className="line-content">
          @@ -{hunk.oldStart},{hunk.oldLines} +{hunk.newStart},{hunk.newLines} @@
          {hunk.header ? ` ${hunk.header}` : ""}
        </td>
      </tr>
      {hunk.lines.map((line, i) => {
        const cls =
          line.type === DiffLineType.ADDED
            ? "line-add"
            : line.type === DiffLineType.REMOVED
              ? "line-del"
              : "line-ctx";
        const marker =
          line.type === DiffLineType.ADDED ? "+" : line.type === DiffLineType.REMOVED ? "−" : " ";
        const anchor = lineAnchor(line);
        const threads = anchor && review ? review.byLine.get(lineKey(path, anchor.side, anchor.line)) : undefined;
        const composerOpen =
          anchor && composerAt && composerAt.side === anchor.side && composerAt.line === anchor.line;
        return (
          <LineWithThreads key={i}>
            <tr className={cls}>
              <td className="gutter">{line.oldLine > 0 ? line.oldLine : ""}</td>
              <td className="gutter gutter-new">
                {line.newLine > 0 ? line.newLine : ""}
                {commentable && anchor && (
                  <button
                    className="line-comment-btn"
                    title="comment on this line (anchored to this version - an amend marks it outdated)"
                    onClick={() => setComposerAt(composerOpen ? null : anchor)}
                  >
                    +
                  </button>
                )}
              </td>
              <td className="line-content">
                {marker} {line.content}
              </td>
            </tr>
            {(threads?.length || composerOpen) && review ? (
              <tr className="thread-row">
                <td colSpan={3}>
                  {threads?.map((t) => (
                    <ThreadCard key={t.root.id} thread={t} actions={review.actions} busy={review.busy} />
                  ))}
                  {composerOpen && anchor && (
                    <CommentComposer
                      placeholder={`Comment on ${path}:${anchor.line}…`}
                      busy={review.busy}
                      onCancel={() => setComposerAt(null)}
                      onSubmit={async (body) => {
                        await review.actions.onComment(body, { path, side: anchor.side, line: anchor.line });
                        setComposerAt(null);
                      }}
                    />
                  )}
                </td>
              </tr>
            ) : null}
          </LineWithThreads>
        );
      })}
    </>
  );
}

// A <tbody> can only contain <tr>; grouping a line with its thread row
// needs a fragment, but fragments need keys from the caller - this tiny
// wrapper keeps the map() readable.
function LineWithThreads({ children }: { children: React.ReactNode }) {
  return <>{children}</>;
}
