import { Link } from "react-router-dom";
import { workspacesClient } from "../api/client";
import { WorkspaceStatus } from "../gen/runko/v1/common_pb";
import { shortSha } from "../lib/format";
import { useRpc } from "../lib/useRpc";
import { EmptyState, ErrorNote, InfoTip, Spinner } from "../components/ui";

const statusLabel: Record<number, string> = {
  [WorkspaceStatus.ACTIVE]: "active",
  [WorkspaceStatus.DETACHED]: "detached",
  [WorkspaceStatus.CLOSED]: "closed",
};

export function WorkspacesPage() {
  const { data, error, loading } = useRpc(
    () => workspacesClient.listWorkspaces({}),
    "workspaces",
  );

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Workspaces</h1>
        <p className="page-sub">
          Registry metadata only — content lives in Git at each snapshot ref (§12.2).
        </p>
      </header>

      {loading && <Spinner />}
      {error && <ErrorNote error={error} />}
      {data && data.workspaces.length === 0 && <EmptyState>No workspaces yet.</EmptyState>}
      {data && data.workspaces.length > 0 && (
        <section className="card">
          <table className="table">
            <thead>
              <tr>
                <th>Workspace</th>
                <th>Owner</th>
                <th>
                  Base
                  <InfoTip text="The trunk revision this workspace was last rebased onto." />
                </th>
                <th>
                  Project affinity
                  <InfoTip text="Which projects this workspace may write to. Writes from an agent are required to stay inside this set - it's enforced server-side at push time, not just a client-side hint." />
                </th>
                <th>
                  Snapshot ref
                  <InfoTip text="The Git ref (refs/workspaces/<id>/head) this workspace's WIP is durably pushed to. Registry rows here are metadata only - the actual content always lives in Git, never only in this database." />
                </th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {data.workspaces.map((w) => (
                <tr key={w.id}>
                  <td className="mono">{w.id}</td>
                  <td>{w.owner}</td>
                  <td className="mono">{shortSha(w.baseRevision)}</td>
                  <td>
                    <span className="chip-row">
                      {w.projectAffinity.map((p) => (
                        <Link className="chip" key={p} to={`/projects/${p}`}>
                          {p}
                        </Link>
                      ))}
                    </span>
                  </td>
                  <td className="mono">{w.snapshotRef}</td>
                  <td>
                    <span
                      className={`chip ${w.status === WorkspaceStatus.ACTIVE ? "chip-green" : ""}`}
                    >
                      {statusLabel[w.status] ?? "unknown"}
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
