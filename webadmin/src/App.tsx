import { useEffect, useState, type FormEvent } from "react";
import {
  adminUser,
  backendUrl,
  createOrg,
  fetchAdminOrgs,
  probeHealth,
  setOrgArchived,
  signedIn,
  signIn,
  signOut,
  type AdminOrgRow,
  type HealthStatus,
} from "./api";

// The deployment admin panel: a SEPARATE app from the main web UI, with
// its own sign-in flow against the dedicated /api/admin surface. It is
// served by its own pod so it stays reachable when the main app - or
// runkod itself - is down; the health strip below is what makes that
// useful (it renders the outage instead of a blank error).
export default function App() {
  return (
    <div className="adm">
      <header className="adm-header">
        <div className="adm-brand">
          <BrandMark />
          Runko <span className="adm-brand-sub">deployment admin</span>
        </div>
        <HealthStrip />
        <div className="adm-header-right">
          {signedIn && (
            <span className="adm-user">
              {adminUser ? (
                <>
                  operator <strong>{adminUser}</strong>
                </>
              ) : (
                "anonymous operator"
              )}
            </span>
          )}
          {signedIn && (
            <button className="btn btn-sm" onClick={() => signOut()}>
              Sign out
            </button>
          )}
          <ThemeToggle />
        </div>
      </header>
      <main className="adm-main">{signedIn ? <Estate /> : <LoginGate />}</main>
    </div>
  );
}

// HealthStrip probes the control plane's unauthenticated liveness and
// readiness endpoints - visible signed out, refreshed continuously, and
// deliberately independent of every other request this app makes.
function HealthStrip() {
  const [health, setHealth] = useState<HealthStatus>({ healthy: null, ready: null });
  useEffect(() => {
    let stale = false;
    const tick = async () => {
      const h = await probeHealth();
      if (!stale) setHealth(h);
    };
    void tick();
    const timer = window.setInterval(() => void tick(), 15000);
    return () => {
      stale = true;
      window.clearInterval(timer);
    };
  }, []);

  const chip = (label: string, state: boolean | null) => (
    <span
      className={
        state === null ? "chip" : state ? "chip chip-green" : "chip chip-red"
      }
      title={state === false ? `${label} probe failed - the control plane may be down` : label}
    >
      {label}: {state === null ? "…" : state ? "up" : "DOWN"}
    </span>
  );

  return (
    <div className="adm-health">
      {chip("runkod", health.healthy)}
      {chip("ready", health.ready)}
    </div>
  );
}

// LoginGate: the dedicated operator sign-in. No org (operators are
// server-global), no sign-up, no session sharing with the main app -
// name + password are an operator principal, or any name with the
// deploy token as the password.
function LoginGate() {
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      await signIn(name.trim(), password);
      // Reloads on success.
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="card login-card" onSubmit={submit}>
        <p className="login-sub">Operator sign-in</p>
        <p className="login-hint">
          This is the deployment admin surface — operator credentials only. The normal app
          sign-in does not work here, and signing in here grants nothing there.
        </p>
        {backendUrl && (
          <p className="login-endpoint" title="The runkod control plane this browser is talking to">
            <code>{backendUrl}</code>
          </p>
        )}
        <label className="login-label">
          Name
          <input
            type="text"
            autoComplete="username"
            autoFocus
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label className="login-label">
          Password
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        <p className="login-hint">
          An operator principal (--principal), or the deploy token as the password with any name.
        </p>
        {error && <div className="login-error">{error}</div>}
        <button className="btn btn-primary login-submit" type="submit" disabled={busy || !password}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
      </form>
    </div>
  );
}

// Estate: the whole org estate - live and archived - with the archive
// lifecycle and operator org creation. Org-LEVEL administration
// (members, merge policy) stays on each org's own settings page in the
// main app; this is the cluster view. The server re-checks operator-
// ness on every call; this client is display only.
function Estate() {
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
    void reload();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

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
        <h1 className="page-title">Org estate</h1>
        <p className="page-sub">
          Every org on this control plane, archived included. Archiving closes an org's whole
          surface but keeps its repo — recovery is one click, never a restore.
        </p>
      </header>

      {loading && <div className="adm-loading">Loading…</div>}
      {error && <div className="login-error">{error}</div>}
      {notice && <div className="adm-notice">{notice}</div>}

      <section className="card">
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
              <tr key={o.name} className={o.archived ? "adm-archived" : undefined}>
                <td className="mono">
                  {o.name}
                  {o.default && <span className="chip adm-chip">default</span>}
                </td>
                <td className="adm-desc">{o.description || "—"}</td>
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
                        onClick={() =>
                          void act(() => setOrgArchived(o.name, false), `${o.name} unarchived.`)
                        }
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

      <section className="card adm-create">
        <h2 className="adm-h2">New org</h2>
        <form
          className="adm-create-form"
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

function ThemeToggle() {
  const [theme, setTheme] = useState<string>(
    () => document.documentElement.dataset.theme ?? "light",
  );
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("runko-theme", theme);
  }, [theme]);
  return (
    <button className="btn btn-sm" onClick={() => setTheme(theme === "dark" ? "light" : "dark")}>
      {theme === "dark" ? "Light" : "Dark"}
    </button>
  );
}

function BrandMark() {
  return (
    <svg width="22" height="22" viewBox="0 0 32 32" aria-hidden>
      <rect width="32" height="32" rx="7" fill="var(--accent)" />
      <line
        x1="16"
        y1="7"
        x2="16"
        y2="25"
        stroke="#fff"
        strokeWidth="2.5"
        strokeLinecap="round"
        opacity="0.55"
      />
      <circle cx="16" cy="7.5" r="3.4" fill="#fff" />
      <circle cx="16" cy="16" r="3.4" fill="#fff" />
      <circle cx="16" cy="24.5" r="3.4" fill="#fff" />
    </svg>
  );
}
