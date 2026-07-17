import { useEffect, useState, type FormEvent } from "react";
import {
  backendUrl,
  fetchAuthConfig,
  pathOrg,
  requestInvite,
  signIn,
  signUp,
  type AuthConfig,
} from "../api/client";

// Sign-in gate for a live (VITE_RUNKO_URL-configured) deployment with no
// credential in this browser yet. Name + password are a runkod principal -
// operator-configured (--principal) or self-registered; the password field
// also accepts the deploy token with any name for an anonymous session.
// When the control plane enables self-service sign-up (--allow-signup,
// §15.1), the gate offers "Create account" - with the invite code field
// only when the daemon demands one. When it also takes invite requests
// (--allow-invite-requests), a third mode asks the operator for the code:
// the request is mailed onward and the reply carries the invite.
export function LoginPage() {
  // ?invite=1 (the landing page's "Request an invite" CTA) deep-links the
  // request mode - and OPTIMISTICALLY: the auth-config fetch is in
  // flight on first render, and gating the form on its result meant the
  // deep link flashed (or, on a failed fetch, permanently showed) the
  // sign-in page instead of the form. Assume enabled until the server
  // says otherwise; a deployment with the intake off falls back to
  // sign-in when the real config lands.
  const deepLinkedInvite = new URLSearchParams(window.location.search).has("invite");
  const [mode, setMode] = useState<"signin" | "signup" | "request">(
    deepLinkedInvite ? "request" : "signin",
  );
  const [config, setConfig] = useState<AuthConfig>({
    signupEnabled: false,
    codeRequired: false,
    orgCreateEnabled: false,
    inviteRequestsEnabled: deepLinkedInvite,
  });
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  // Sessions are org-scoped (2026-07-09): you sign in TO an org. Signing
  // in from inside an org's own /<org> URL means THAT org (anything else
  // invites the cross-org rebind lib/orgsession.ts documents); elsewhere,
  // prefill the last org this browser used.
  const [org, setOrg] = useState(() => pathOrg || window.localStorage.getItem("runko-org") || "");
  const [orgMode, setOrgMode] = useState<"create" | "join">("create");
  // Invite-request mode's own fields; `website` is the honeypot (rendered
  // off-screen, humans leave it empty).
  const [email, setEmail] = useState("");
  const [message, setMessage] = useState("");
  const [website, setWebsite] = useState("");
  const [requested, setRequested] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void fetchAuthConfig().then(setConfig);
  }, []);

  const signingUp = mode === "signup" && config.signupEnabled;
  const requesting = mode === "request" && config.inviteRequestsEnabled;

  const switchMode = (next: "signin" | "signup" | "request") => {
    setMode(next);
    setError(undefined);
    setRequested(false);
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(undefined);
    try {
      if (requesting) {
        await requestInvite(name.trim(), email.trim(), message, website);
        setRequested(true);
        setBusy(false);
      } else if (signingUp) {
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
      // Sign-in and sign-up reload the page on success.
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
          {requesting
            ? "Ask for an invite to this control plane"
            : signingUp
              ? "Create an account on this control plane"
              : "Sign in to this control plane"}
        </p>
        {backendUrl && (
          <p className="login-endpoint" title="The runkod control plane this browser is talking to">
            <code>{backendUrl}</code>
          </p>
        )}
        {requesting && requested ? (
          <>
            <p className="login-hint">
              Request sent — the invite code arrives as a reply to <strong>{email.trim()}</strong>.
            </p>
            <p className="login-foot">
              Got the code?{" "}
              <button type="button" className="login-link" onClick={() => switchMode("signup")}>
                Create your account
              </button>
            </p>
          </>
        ) : (
          <>
            {!signingUp && !requesting && (
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
                autoComplete={requesting ? "name" : "username"}
                autoFocus={requesting || !!org}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            {!requesting && (
              <label className="login-label">
                Password
                <input
                  type="password"
                  autoComplete={signingUp ? "new-password" : "current-password"}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </label>
            )}
            {requesting && (
              <>
                <label className="login-label">
                  Email
                  <input
                    type="email"
                    autoComplete="email"
                    value={email}
                    placeholder="you@example.com"
                    onChange={(e) => setEmail(e.target.value)}
                  />
                </label>
                <label className="login-label">
                  Why do you want access? (optional)
                  <textarea
                    maxLength={2000}
                    value={message}
                    onChange={(e) => setMessage(e.target.value)}
                  />
                </label>
                {/* Honeypot: off-screen, skipped by keyboard, ignored by
                    humans - the server silently drops a filled value. */}
                <label className="login-honeypot" aria-hidden="true">
                  Website
                  <input
                    type="text"
                    tabIndex={-1}
                    autoComplete="off"
                    value={website}
                    onChange={(e) => setWebsite(e.target.value)}
                  />
                </label>
                <p className="login-hint">
                  The operator replies to your email with the invite code.
                </p>
              </>
            )}
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
            {signingUp && config.codeRequired && config.inviteRequestsEnabled && (
              <p className="login-hint">
                No invite code?{" "}
                <button type="button" className="login-link" onClick={() => switchMode("request")}>
                  Request access
                </button>
              </p>
            )}
            {error && <div className="login-error">{error}</div>}
            <button
              className="btn btn-primary login-submit"
              type="submit"
              disabled={
                busy ||
                (requesting
                  ? !name.trim() || !email.trim()
                  : !password || !org.trim() || (signingUp && password.length < 8))
              }
            >
              {busy
                ? requesting
                  ? "Sending…"
                  : signingUp
                    ? "Creating account…"
                    : "Signing in…"
                : requesting
                  ? "Request an invite"
                  : signingUp
                    ? "Create account"
                    : "Sign in"}
            </button>
          </>
        )}
        {requesting ? (
          <p className="login-foot">
            Already have an account?{" "}
            <button type="button" className="login-link" onClick={() => switchMode("signin")}>
              Sign in
            </button>
          </p>
        ) : (
          config.signupEnabled && (
            <p className="login-foot">
              {signingUp ? (
                <>
                  Already have an account?{" "}
                  <button type="button" className="login-link" onClick={() => switchMode("signin")}>
                    Sign in
                  </button>
                </>
              ) : (
                <>
                  New here?{" "}
                  <button type="button" className="login-link" onClick={() => switchMode("signup")}>
                    Create an account
                  </button>
                  {config.inviteRequestsEnabled && (
                    <>
                      {" · "}
                      <button
                        type="button"
                        className="login-link"
                        onClick={() => switchMode("request")}
                      >
                        Request an invite
                      </button>
                    </>
                  )}
                </>
              )}
            </p>
          )
        )}
        <p className="login-foot">
          Credentials stay in this browser. <a href="/landing/">What is Runko?</a>
        </p>
      </form>
    </div>
  );
}
