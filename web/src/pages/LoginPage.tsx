import { useEffect, useState, type FormEvent } from "react";
import {
  backendUrl,
  browsePublicOrg,
  fetchAuthConfig,
  fetchOrgs,
  signIn,
  signUp,
  type AuthConfig,
  type OrgInfo,
} from "../api/client";

// Sign-in gate for a live (VITE_RUNKO_URL-configured) deployment with no
// credential in this browser yet. Name + password are a runkod principal -
// operator-configured (--principal) or self-registered; the password field
// also accepts the deploy token with any name for an anonymous session.
// When the control plane enables self-service sign-up (--allow-signup,
// §15.1), the gate offers "Create account" - with the invite code field
// only when the daemon demands one.
export function LoginPage() {
  const [mode, setMode] = useState<"signin" | "signup">("signin");
  const [config, setConfig] = useState<AuthConfig>({
    signupEnabled: false,
    codeRequired: false,
    orgCreateEnabled: false,
  });
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  // Sessions are org-scoped (2026-07-09): you sign in TO an org. Prefill
  // the last one this browser used.
  const [org, setOrg] = useState(() => window.localStorage.getItem("runko-org") ?? "");
  const [orgMode, setOrgMode] = useState<"create" | "join">("create");
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);

  // Public orgs (§15.2): anonymous GET /api/orgs lists exactly the
  // public_read set, so the gate can offer read-only browsing without an
  // account at all.
  const [publicOrgs, setPublicOrgs] = useState<OrgInfo[]>([]);
  useEffect(() => {
    void fetchAuthConfig().then(setConfig);
    void fetchOrgs().then(setPublicOrgs);
  }, []);

  const signingUp = mode === "signup" && config.signupEnabled;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      if (signingUp) {
        await signUp(
          name.trim(),
          password,
          code.trim(),
          org.trim(),
          config.orgCreateEnabled ? orgMode : "join",
        );
      } else {
        await signIn(name.trim(), password, org.trim());
      }
      // Both reload the page on success.
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="card login-card" onSubmit={submit}>
        <div className="sidebar-brand login-brand">Runko</div>
        <p className="login-sub">
          {signingUp ? "Create an account on this control plane" : "Sign in to this control plane"}
        </p>
        {backendUrl && (
          <p className="login-endpoint" title="The runkod control plane this browser is talking to">
            <code>{backendUrl}</code>
          </p>
        )}
        {!signingUp && publicOrgs.length > 0 && (
          <p className="login-sub">
            Or browse without an account:{" "}
            {publicOrgs.map((o) => (
              <button
                key={o.name}
                type="button"
                className="btn btn-sm"
                onClick={() => browsePublicOrg(o.name)}
              >
                {o.name} (read-only)
              </button>
            ))}
          </p>
        )}
        {!signingUp && (
          <label className="login-label">
            Organization
            <input
              type="text"
              autoComplete="organization"
              autoFocus={!org}
              value={org}
              placeholder="your org's name"
              onChange={(e) => setOrg(e.target.value)}
            />
          </label>
        )}
        <label className="login-label">
          Name
          <input
            type="text"
            autoComplete="username"
            autoFocus={!!org}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </label>
        <label className="login-label">
          Password
          <input
            type="password"
            autoComplete={signingUp ? "new-password" : "current-password"}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {signingUp && (
          <p className="login-hint">At least 8 characters — it's your only credential here.</p>
        )}
        {signingUp && (
          <>
            {config.orgCreateEnabled && (
              <div className="login-orgmode" role="radiogroup" aria-label="Organization">
                <label className={orgMode === "create" ? "login-radio active" : "login-radio"}>
                  <input
                    type="radio"
                    name="org-mode"
                    checked={orgMode === "create"}
                    onChange={() => setOrgMode("create")}
                  />
                  Create a new org
                </label>
                <label className={orgMode === "join" ? "login-radio active" : "login-radio"}>
                  <input
                    type="radio"
                    name="org-mode"
                    checked={orgMode === "join"}
                    onChange={() => setOrgMode("join")}
                  />
                  Join an existing org
                </label>
              </div>
            )}
            <label className="login-label">
              Organization
              <input
                type="text"
                autoComplete="organization"
                value={org}
                placeholder="e.g. acme"
                onChange={(e) => setOrg(e.target.value)}
              />
            </label>
            <p className="login-hint">
              {config.orgCreateEnabled && orgMode === "create"
                ? "Your org gets its own repo — you'll be its admin."
                : "Anyone can join an existing org for now; ask a teammate for the name."}
            </p>
          </>
        )}
        {signingUp && config.codeRequired && (
          <label className="login-label">
            Invite code
            <input
              type="text"
              autoComplete="off"
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
          </label>
        )}
        {error && <div className="login-error">{error}</div>}
        <button
          className="btn btn-primary login-submit"
          type="submit"
          disabled={busy || !password || !org.trim() || (signingUp && password.length < 8)}
        >
          {busy
            ? signingUp
              ? "Creating account…"
              : "Signing in…"
            : signingUp
              ? "Create account"
              : "Sign in"}
        </button>
        {config.signupEnabled && (
          <p className="login-foot">
            {signingUp ? (
              <>
                Already have an account?{" "}
                <button type="button" className="login-link" onClick={() => setMode("signin")}>
                  Sign in
                </button>
              </>
            ) : (
              <>
                New here?{" "}
                <button type="button" className="login-link" onClick={() => setMode("signup")}>
                  Create an account
                </button>
              </>
            )}
          </p>
        )}
        <p className="login-foot">
          Credentials stay in this browser. <a href="/landing/">What is Runko?</a>
        </p>
      </form>
    </div>
  );
}
