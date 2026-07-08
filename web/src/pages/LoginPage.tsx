import { useState, type FormEvent } from "react";
import { signIn } from "../api/client";

// Sign-in gate for a live (VITE_RUNKO_URL-configured) deployment with no
// credential in this browser yet. Name + password are a runkod named
// principal (--principal name=…;token=…); the password field also accepts
// the deploy token with any name for an anonymous session (the documented
// eval credential). Validation is one GET /api/whoami round-trip - see
// api/client.ts signIn.
export function LoginPage() {
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
      // signIn reloads the page on success.
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <div className="login-wrap">
      <form className="card login-card" onSubmit={submit}>
        <div className="sidebar-brand login-brand">Runko</div>
        <p className="login-sub">Sign in to this control plane</p>
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
        {error && <div className="login-error">{error}</div>}
        <button className="btn btn-primary login-submit" type="submit" disabled={busy || !password}>
          {busy ? "Signing in…" : "Sign in"}
        </button>
        <p className="login-foot">
          Credentials stay in this browser. Just looking?{" "}
          <a href="/demo/changes">Explore the demo</a>
        </p>
      </form>
    </div>
  );
}
