import { useEffect, useState } from "react";
import {
  createOrg,
  fetchAdminOrgs,
  isOperator,
  setOrgArchived,
  usingDemoData,
  type AdminOrgRow,
} from "../api/client";
import { EmptyState, Spinner } from "../components/ui";

// Deployment admin panel (operator-only; runkod/orghub.go's /api/admin
// surface): the whole org estate - live and archived - with the archive
// lifecycle and org creation. Org-LEVEL administration (members, merge
// policy) stays on each org's own settings page; this is the cluster
// view. The server re-checks operator-ness on every call; the client
// gate is display only.
export function AdminPage() {
  const [rows, setRows] = useState<AdminOrgRow[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [newOrg, setNewOrg] = useState("");

  const reload = async () => {
    setError(null);
    try {
      setRows(await fetchAdminOrgs());
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    if (!usingDemoData && isOperator) void reload();
    else setLoading(false);
  }, []);

  if (usingDemoData || !isOperator) {
    return (
      <div className="page">
        <header className="page-header">
          <h1 className="page-title">Deployment admin</h1>
        </header>
        <EmptyState>
          {usingDemoData
            ? "The admin panel is a live-control-plane surface — not part of the playground."
            : "Operator credentials only — sign in with an operator principal or the deploy token."}
        </EmptyState>
      </div>
    );
  }

  const act = async (fn: () => Promise<unknown>, done: string) => {
    setError(null);
    setNotice(null);
    try {
      await fn();
      setNotice(done);
      await reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Deployment admin</h1>
        <p className="page-sub">
          Every org on this control plane, archived included. Archiving closes an org's whole
          surface but keeps its repo — recovery is one click, never a restore.
        </p>
      </header>

      {loading && <Spinner />}
      {error && <div className="login-error">{error}</div>}
      {notice && <div className="settings-notice">{notice}</div>}

      <section className="card settings-card admin-estate">
        <table className="table">
          <thead>
            <tr>
              <th>Org</th>
              <th>Description</th>
              <th>Members</th>
              <th>State</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {rows.map((o) => (
              <tr key={o.name} className={o.archived ? "admin-archived" : undefined}>
                <td className="mono">
                  {o.name}
                  {o.default && <span className="chip admin-chip">default</span>}
                </td>
                <td className="admin-desc">{o.description || "—"}</td>
                <td>{(o.members ?? []).join(", ") || "—"}</td>
                <td>
                  {o.archived ? (
                    <span className="chip chip-amber">archived</span>
                  ) : (
                    <span className="chip chip-green">live</span>
                  )}
                </td>
                <td>
                  {!o.default &&
                    (o.archived ? (
                      <button
                        className="btn btn-sm"
                        onClick={() => void act(() => setOrgArchived(o.name, false), `${o.name} unarchived.`)}
                      >
                        Unarchive
                      </button>
                    ) : (
                      <button
                        className="btn btn-sm btn-danger"
                        onClick={() =>
                          window.confirm(
                            `Archive ${o.name}? Its whole surface (web, API, git) goes offline until unarchived. The repo is kept.`,
                          ) && void act(() => setOrgArchived(o.name, true), `${o.name} archived.`)
                        }
                      >
                        Archive
                      </button>
                    ))}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      <section className="card settings-card">
        <h2 className="settings-h2">New org</h2>
        <form
          className="settings-add-member"
          onSubmit={(e) => {
            e.preventDefault();
            if (!newOrg.trim()) return;
            void act(async () => {
              await createOrg(newOrg.trim());
              setNewOrg("");
            }, `${newOrg.trim()} created.`);
          }}
        >
          <input
            className="settings-input"
            type="text"
            value={newOrg}
            placeholder="lowercase letters, digits, dashes"
            onChange={(e) => setNewOrg(e.target.value)}
          />
          <button className="btn btn-primary" type="submit">
            Create org
          </button>
        </form>
      </section>
    </div>
  );
}
