// Client for the DEDICATED deployment-admin surface (/api/admin/*,
// runkod/orghub.go). This panel never rides the org-scoped API or the
// main web app's sign-in flow: the credential here is an OPERATOR one
// (a config principal or the deploy token), validated against
// GET /api/admin/whoami and stored per-browser under its own keys, so
// an org session in the main app and an operator session here never
// mix. Health probes are unauthenticated by design - the panel must be
// able to say "runkod is down" precisely when runkod is down.

const rawBaseUrl: string | undefined = import.meta.env.VITE_RUNKO_URL;

// "/" (or unset) means same-origin: the deployed pod sits behind the
// path-routed ingress that sends /api to runkod and /admin here. An
// absolute URL still works for the dev-server-to-local-daemon loop.
export const baseUrl = new URL(rawBaseUrl || "/", window.location.origin).toString();

/** The control-plane origin this browser talks to, surfaced on the
 * sign-in screen so it's never ambiguous which deployment you're
 * administering. */
export const backendUrl: string = new URL(baseUrl).origin;

const storedUser: string | null = window.localStorage.getItem("runko-admin-user");
const storedBasic: string | null = window.localStorage.getItem("runko-admin-basic");

/** True when this browser holds an operator credential. Display state -
 * every admin endpoint re-checks server-side on every call. */
export const signedIn = !!storedBasic;

/** The operator's name; "" when signed in with the anonymous deploy
 * token. */
export const adminUser: string = storedBasic ? (storedUser ?? "") : "";

function authHeaders(): Record<string, string> {
  if (storedBasic) return { Authorization: `Basic ${storedBasic}` };
  return {};
}

/** Decode runkod's structured error body ({Code, Message, Suggestion} -
 * Go-exported names per docs/cli-contract.md) into a thrown Error. */
async function throwStructured(res: Response, fallback: string): Promise<never> {
  let msg = `${fallback} (HTTP ${res.status})`;
  try {
    const e = (await res.json()) as { Message?: string; Suggestion?: string };
    if (e.Message) msg = e.Message + (e.Suggestion ? ` — ${e.Suggestion}` : "");
  } catch {
    // plain-text body; keep the status message
  }
  throw new Error(msg);
}

/** Operator sign-in: one round trip to the dedicated admin whoami. 401 =
 * wrong credential, 403 = valid but not an operator (the server tells
 * the two apart so we can too). Stores the credential and reloads. */
export async function signIn(name: string, password: string): Promise<void> {
  const basic = btoa(`${name}:${password}`);
  const res = await fetch(new URL("api/admin/whoami", baseUrl), {
    headers: { Authorization: `Basic ${basic}` },
  });
  if (res.status === 401) throw new Error("wrong name or password");
  if (res.status === 403) await throwStructured(res, "not an operator credential");
  if (!res.ok) throw new Error(`runkod answered HTTP ${res.status}`);
  const who = (await res.json()) as { name?: string; anonymous?: boolean };
  window.localStorage.setItem("runko-admin-user", who.anonymous ? "" : (who.name ?? name));
  window.localStorage.setItem("runko-admin-basic", basic);
  window.location.reload();
}

/** Clear this browser's operator credential and reload. */
export function signOut(): void {
  window.localStorage.removeItem("runko-admin-user");
  window.localStorage.removeItem("runko-admin-basic");
  window.location.reload();
}

export interface AdminOrgRow {
  name: string;
  description: string;
  members: string[] | null;
  archived: boolean;
  default: boolean;
}

/** The deployment's whole org estate, archived included. */
export async function fetchAdminOrgs(): Promise<AdminOrgRow[]> {
  const res = await fetch(new URL("api/admin/orgs", baseUrl), { headers: authHeaders() });
  if (!res.ok) await throwStructured(res, "loading the org estate failed");
  const d = (await res.json()) as { orgs?: AdminOrgRow[] };
  return d.orgs ?? [];
}

/** Create an org operator-side (POST /api/admin/orgs - not gated by
 * --allow-org-create; that flag scopes signup accounts). */
export async function createOrg(name: string): Promise<void> {
  const res = await fetch(new URL("api/admin/orgs", baseUrl), {
    method: "POST",
    headers: { ...authHeaders(), "Content-Type": "application/json" },
    body: JSON.stringify({ name }),
  });
  if (!res.ok) await throwStructured(res, "creating org failed");
}

/** Archive/unarchive an org. Archived orgs keep row + repo; their whole
 * surface answers 410 until unarchived. */
export async function setOrgArchived(org: string, archived: boolean): Promise<void> {
  const res = await fetch(
    new URL(`api/admin/orgs/${org}/${archived ? "archive" : "unarchive"}`, baseUrl),
    { method: "POST", headers: authHeaders() },
  );
  if (!res.ok) await throwStructured(res, "changing archive state failed");
}

/** Control-plane health: null = probing, true/false = the probe's
 * answer. healthy is /healthz (process up, repo where expected); ready
 * is /readyz (dependencies answer too). Unauthenticated - works signed
 * out, works while everything else is on fire. */
export interface HealthStatus {
  healthy: boolean | null;
  ready: boolean | null;
}

export async function probeHealth(): Promise<HealthStatus> {
  const probe = async (path: string): Promise<boolean> => {
    try {
      const res = await fetch(new URL(path, baseUrl), { cache: "no-store" });
      return res.ok;
    } catch {
      return false;
    }
  };
  const [healthy, ready] = await Promise.all([probe("healthz"), probe("readyz")]);
  return { healthy, ready };
}
