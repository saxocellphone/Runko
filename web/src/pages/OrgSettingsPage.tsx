import { useEffect, useState } from "react";
import {
  addOrgMember,
  authUser,
  currentOrg,
  fetchOrgMembers,
  fetchOrgs,
  fetchOrgSettings,
  removeOrgMember,
  updateOrgSettings,
  usingDemoData,
  type OrgInfo,
  type OrgMember,
} from "../api/client";
import { EmptyState, Spinner } from "../components/ui";

// Org settings (multi-org, runkod/orghub.go): description + org-required
// checks (enforced at the §13.5 merge gate, merged with daemon flag
// config) + member management. Reads are open to members (and to every
// credential on the shared default org); writes need an org admin or an
// operator - the server enforces this, the UI just mirrors it.
export function OrgSettingsPage() {
  const [info, setInfo] = useState<OrgInfo | null>(null);
  const [members, setMembers] = useState<OrgMember[]>([]);
  const [description, setDescription] = useState("");
  const [checksText, setChecksText] = useState("");
  const [publicRead, setPublicRead] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [newMember, setNewMember] = useState("");
  const [newRole, setNewRole] = useState("member");

  const reload = async () => {
    setError(null);
    try {
      const orgs = await fetchOrgs();
      const current =
        (currentOrg ? orgs.find((o) => o.name === currentOrg) : orgs.find((o) => o.default)) ??
        null;
      setInfo(current);
      if (!current) return;
      const [settings, mem] = await Promise.all([
        fetchOrgSettings(current.name),
        fetchOrgMembers(current.name).catch(() => []),
      ]);
      setDescription(settings.description ?? "");
      setChecksText((settings.global_required_checks ?? []).join(", "));
      setPublicRead(!!settings.public_read);
      setMembers(mem);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    if (!usingDemoData) void reload();
    else setLoading(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (usingDemoData) {
    return (
      <div className="page">
        <header className="page-header">
          <h1 className="page-title">Org settings</h1>
        </header>
        <EmptyState>Org settings are a live-control-plane surface — not part of the playground.</EmptyState>
      </div>
    );
  }

  const isAdmin = info?.role === "admin" || info?.role === "operator";
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

  const saveSettings = () =>
    act(
      () =>
        updateOrgSettings(info!.name, {
          description: description.trim(),
          global_required_checks: checksText
            .split(",")
            .map((s) => s.trim())
            .filter(Boolean),
          public_read: publicRead,
        }),
      "Settings saved.",
    );

  return (
    <div className="page">
      <header className="page-header">
        <h1 className="page-title">Org settings{info ? ` — ${info.name}` : ""}</h1>
        <p className="page-sub">
          {info?.default
            ? "The shared default org: everyone with an account can use it; its admins manage these settings."
            : "Membership controls who can reach this org at all — API and git alike."}
        </p>
      </header>

      {loading && <Spinner />}
      {error && <div className="login-error">{error}</div>}
      {notice && <div className="settings-notice">{notice}</div>}

      {info && (
        <>
          <section className="card settings-card">
            <h2 className="settings-h2">About</h2>
            <div className="settings-row">
              <span className="settings-label">Your role</span>
              <span className="chip">{info.role}</span>
            </div>
            <div className="settings-row">
              <span className="settings-label">Git remote</span>
              <code className="settings-code">
                {new URL(info.git_url.replace(/^\//, ""), window.location.origin + "/").toString()}
              </code>
            </div>
            {!info.default && (
              <div className="settings-row">
                <span className="settings-label">API base</span>
                <code className="settings-code">
                  {new URL(info.api_base.replace(/^\//, ""), window.location.origin + "/").toString()}
                </code>
              </div>
            )}
            <label className="settings-label" htmlFor="org-desc">
              Description
            </label>
            <textarea
              id="org-desc"
              className="settings-input"
              rows={2}
              value={description}
              disabled={!isAdmin}
              placeholder={isAdmin ? "What this org is for" : "(none)"}
              onChange={(e) => setDescription(e.target.value)}
            />
          </section>

          <section className="card settings-card">
            <h2 className="settings-h2">Visibility</h2>
            <label className="settings-label" htmlFor="org-public">
              <input
                id="org-public"
                type="checkbox"
                checked={publicRead}
                disabled={!isAdmin}
                onChange={(e) => setPublicRead(e.target.checked)}
              />{" "}
              Public read-only access
            </label>
            <p className="settings-hint">
              Anyone can clone the repo, read changes/projects/search, and browse the
              read-only UI at{" "}
              <code className="settings-code">
                {new URL(`${info.name}`, window.location.origin + "/").toString()}
              </code>
              . Writes, workspaces, and these settings stay members-only. Cannot be
              enabled while any project declares <code>visibility: restricted</code>.
            </p>
          </section>

          <section className="card settings-card">
            <h2 className="settings-h2">Merge policy</h2>
            <label className="settings-label" htmlFor="org-checks">
              Org-required checks
            </label>
            <input
              id="org-checks"
              className="settings-input"
              type="text"
              value={checksText}
              disabled={!isAdmin}
              placeholder={isAdmin ? "e.g. secrets-scan, lint (comma-separated)" : "(none)"}
              onChange={(e) => setChecksText(e.target.value)}
            />
            <p className="settings-hint">
              Required on <strong>every</strong> change in this org, on top of each project's own{" "}
              <code>ci.checks</code>. Takes effect immediately at the merge gate.
            </p>
            {isAdmin && (
              <button className="btn btn-primary" onClick={() => void saveSettings()}>
                Save settings
              </button>
            )}
          </section>

          <section className="card settings-card">
            <h2 className="settings-h2">Members</h2>
            {info.default && (
              <p className="settings-hint">
                The shared org is open to every account; membership rows here only grant{" "}
                <strong>admin</strong> rights over these settings.
              </p>
            )}
            {members.length === 0 ? (
              <EmptyState>No membership rows.</EmptyState>
            ) : (
              <table className="table">
                <thead>
                  <tr>
                    <th>Account</th>
                    <th>Role</th>
                    {isAdmin && <th />}
                  </tr>
                </thead>
                <tbody>
                  {members.map((m) => (
                    <tr key={m.name}>
                      <td>
                        {m.name}
                        {m.name === authUser ? " (you)" : ""}
                      </td>
                      <td>
                        {isAdmin ? (
                          <select
                            className="settings-select"
                            value={m.role}
                            onChange={(e) =>
                              void act(
                                () => addOrgMember(info.name, m.name, e.target.value),
                                `${m.name} is now ${e.target.value}.`,
                              )
                            }
                          >
                            <option value="member">member</option>
                            <option value="admin">admin</option>
                          </select>
                        ) : (
                          m.role
                        )}
                      </td>
                      {isAdmin && (
                        <td>
                          <button
                            className="btn btn-sm"
                            onClick={() =>
                              window.confirm(`Remove ${m.name} from ${info.name}?`) &&
                              void act(() => removeOrgMember(info.name, m.name), `${m.name} removed.`)
                            }
                          >
                            Remove
                          </button>
                        </td>
                      )}
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            {isAdmin && (
              <form
                className="settings-add-member"
                onSubmit={(e) => {
                  e.preventDefault();
                  if (!newMember.trim()) return;
                  void act(async () => {
                    await addOrgMember(info.name, newMember.trim(), newRole);
                    setNewMember("");
                  }, `${newMember.trim()} added as ${newRole}.`);
                }}
              >
                <input
                  className="settings-input"
                  type="text"
                  value={newMember}
                  placeholder="account name (must have signed up)"
                  onChange={(e) => setNewMember(e.target.value)}
                />
                <select
                  className="settings-select"
                  value={newRole}
                  onChange={(e) => setNewRole(e.target.value)}
                >
                  <option value="member">member</option>
                  <option value="admin">admin</option>
                </select>
                <button className="btn btn-primary" type="submit">
                  Add member
                </button>
              </form>
            )}
          </section>
        </>
      )}
    </div>
  );
}
