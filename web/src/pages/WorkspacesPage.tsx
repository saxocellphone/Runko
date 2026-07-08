import { Link } from "react-router-dom";
import { changesClient, workspacesClient } from "../api/client";
import { ChangeState, WorkspaceStatus } from "../gen/runko/v1/common_pb";
import { shortSha } from "../lib/format";
import { changesByOrigin } from "../lib/stacks";
import { useRpc } from "../lib/useRpc";
import { EmptyState, ErrorNote, InfoTip, Spinner } from "../components/ui";

const statusLabel: Record<number, string> = {
  [WorkspaceStatus.ACTIVE]: "active",
  [WorkspaceStatus.DETACHED]: "detached",
  [WorkspaceStatus.CLOSED]: "closed",
};

export function WorkspacesPage() {
  const { data, error, loading } = useRpc(async () => {
    // Open changes join to workspaces via their recorded push provenance
    // (§12.2): each workspace branch is expected to carry exactly one
    // stack, and this page is where that mapping is made visible.
    const [ws, open] = await Promise.all([
      workspacesClient.listWorkspaces({}),
      changesClient.listChanges({ state: ChangeState.OPEN }),
    ]);
    return { workspaces: ws.workspaces, stacks: changesByOrigin(open.changes) };
  }, "workspaces");

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Workspaces</h1>
        <p className="page-sub">
          Registry metadata only — content lives in Git at each snapshot ref (§12.2). Each
          branch carries one stack of changes.
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
                  Branches → stacks
                  <InfoTip text="Parallel lines of work inside this one workspace: each branch is a Git ref (refs/workspaces/<id>/<branch>) WIP is durably pushed to; 'head' is the default. One branch carries one stack - the open changes listed under each branch were pushed from it (recorded at push time, validated against this registry)." />
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
                  <td>
                    {w.branches.length === 0 && <span className="chip">none yet</span>}
                    {w.branches.map((b) => (
                      <BranchStack
                        key={b}
                        branch={b}
                        stack={data.stacks.get(`${w.id}/${b}`) ?? []}
                      />
                    ))}
                  </td>
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

// BranchStack renders one workspace branch with the stack of open changes
// pushed from it - base-most at the bottom, matching the changes inbox.
function BranchStack({
  branch,
  stack,
}: {
  branch: string;
  stack: { id: string; title: string }[];
}) {
  return (
    <div className="ws-branch">
      <span className="chip mono">{branch}</span>
      {stack.length === 0 ? (
        <span className="ws-branch-empty">no open changes</span>
      ) : (
        <ul className="ws-branch-stack">
          {[...stack].reverse().map((c) => (
            <li key={c.id}>
              <Link className="ws-branch-change" to={`/changes/${c.id}`}>
                {c.title}
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
