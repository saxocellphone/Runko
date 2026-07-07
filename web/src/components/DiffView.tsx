import { useState } from "react";
import { Link } from "react-router-dom";
import {
  DiffLineType,
  FileDiffStatus,
  type DiffHunk,
  type FileDiff,
} from "../gen/runko/v1/changes_pb";

export function DiffView({ files }: { files: FileDiff[] }) {
  const additions = files.reduce((n, f) => n + f.additions, 0);
  const deletions = files.reduce((n, f) => n + f.deletions, 0);
  return (
    <div>
      <div className="diff-summary">
        <span>
          {files.length} file{files.length === 1 ? "" : "s"} changed
        </span>
        <span className="added-count">+{additions}</span>
        <span className="deleted-count">−{deletions}</span>
      </div>
      {files.map((f) => (
        <FileDiffCard key={f.path} file={f} />
      ))}
    </div>
  );
}

const statusChip: Record<number, { label: string; cls: string } | undefined> = {
  [FileDiffStatus.ADDED]: { label: "added", cls: "chip-green" },
  [FileDiffStatus.DELETED]: { label: "deleted", cls: "chip-red" },
  [FileDiffStatus.RENAMED]: { label: "renamed", cls: "chip-violet" },
};

function FileDiffCard({ file }: { file: FileDiff }) {
  const [collapsed, setCollapsed] = useState(false);
  const chip = statusChip[file.status];
  return (
    <section className={`card file-diff${collapsed ? " collapsed" : ""}`}>
      <header className="file-head" onClick={() => setCollapsed(!collapsed)}>
        <span className="chevron">{collapsed ? "▶" : "▼"}</span>
        <span className="file-path">
          {file.oldPath && <span className="file-old-path">{file.oldPath}</span>}
          {file.path}
        </span>
        {chip && <span className={`chip ${chip.cls}`}>{chip.label}</span>}
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
      {!collapsed &&
        (file.binary ? (
          <div className="binary-note">Binary file not shown</div>
        ) : (
          <table className="hunk-table">
            <tbody>
              {file.hunks.map((h, i) => (
                <HunkRows key={i} hunk={h} />
              ))}
            </tbody>
          </table>
        ))}
    </section>
  );
}

function HunkRows({ hunk }: { hunk: DiffHunk }) {
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
        return (
          <tr className={cls} key={i}>
            <td className="gutter">{line.oldLine > 0 ? line.oldLine : ""}</td>
            <td className="gutter">{line.newLine > 0 ? line.newLine : ""}</td>
            <td className="line-content">
              {marker} {line.content}
            </td>
          </tr>
        );
      })}
    </>
  );
}
