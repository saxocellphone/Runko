// Service clients for the runko.v1 services (proto/runko/v1/).
//
// Transport selection, decided once at page load:
//   - Any URL under /demo serves the in-memory fake transport (the demo
//     fixture scene) regardless of configuration - main.tsx mounts the app
//     under the /demo basename, so the demo stays reachable side by side
//     with a real control plane.
//   - Everywhere else, VITE_RUNKO_URL selects a real Connect transport
//     against runkod's connect-go handlers (runkod/rpc.go), with
//     VITE_RUNKO_TOKEN carried as the same Authorization bearer every other
//     client sends. Unset, the fake transport serves the root app too -
//     same generated types, same call sites.
import { createClient, type Interceptor } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { ChangeService } from "../gen/runko/v1/changes_pb";
import { ProjectService } from "../gen/runko/v1/projects_pb";
import { RepoService } from "../gen/runko/v1/repo_pb";
import { SearchService } from "../gen/runko/v1/search_pb";
import { WorkspaceService } from "../gen/runko/v1/workspaces_pb";
import { createFakeTransport } from "./fake/transport";

const rawBaseUrl: string | undefined = import.meta.env.VITE_RUNKO_URL;

// "/" (or any relative value) means same-origin: the deployed image sits
// behind a path-routed ingress that sends /runko.v1.* to runkod and
// everything else here, so browser calls never cross origins. Absolute
// URLs still work for the dev-server-to-local-daemon loop.
const baseUrl =
  rawBaseUrl && typeof window !== "undefined"
    ? new URL(rawBaseUrl, window.location.origin).toString()
    : rawBaseUrl;

// Sign-in state is per BROWSER at runtime (localStorage), never baked at
// build time - Vite inlines build vars into the public bundle, which
// would hand the credential to every visitor. Basic auth (name + the
// principal's password) is the human flow, resolved server-side against
// runkod's named-principal registry (§15.1 interim; runkod/auth.go);
// VITE_RUNKO_TOKEN remains as an anonymous bearer fallback for the local
// dev loop only.
const storedUser: string | null =
  typeof window !== "undefined" ? window.localStorage.getItem("runko-user") : null;
const storedBasic: string | null =
  typeof window !== "undefined" ? window.localStorage.getItem("runko-basic") : null;
const devToken: string | undefined = import.meta.env.VITE_RUNKO_TOKEN;

/** True when the current page lives under the /demo mount (see main.tsx). */
export const onDemoRoute =
  typeof window !== "undefined" &&
  (window.location.pathname === "/demo" || window.location.pathname.startsWith("/demo/"));

export const usingDemoData = onDemoRoute || !baseUrl;

/** The signed-in principal's name; null when not signed in OR signed in
 * anonymously (deploy-token password). The server re-derives identity
 * from the credential on every call - this is display state, not
 * authority. */
export const authUser: string | null =
  !usingDemoData && storedBasic && storedUser ? storedUser : null;

/** True when this browser holds a credential (named or anonymous). */
export const signedIn = !usingDemoData && !!storedBasic;

/** Live transport configured but no credential in this browser yet: every
 * RPC will 401, so App gates on the sign-in screen. */
export const liveUnauthenticated = !usingDemoData && !storedBasic && !devToken;

const auth: Interceptor = (next) => (req) => {
  if (storedBasic) req.header.set("Authorization", `Basic ${storedBasic}`);
  else if (devToken) req.header.set("Authorization", `Bearer ${devToken}`);
  return next(req);
};

// --- org selection (multi-org, runkod/orghub.go) ---------------------
// Each org mounts the identical Connect/REST surface under /o/<org>/;
// the selected org is per-browser state, "" meaning the default org at
// the root mount. Account APIs (whoami, signup, orgs) stay at root -
// accounts are server-global.
const storedOrg: string | null =
  typeof window !== "undefined" ? window.localStorage.getItem("runko-org") : null;
export const currentOrg: string = !usingDemoData && storedOrg ? storedOrg : "";

const transportBase =
  currentOrg && baseUrl ? new URL(`o/${currentOrg}/`, baseUrl).toString() : baseUrl;

export interface OrgInfo {
  name: string;
  role: string;
  api_base: string;
  git_url: string;
  default?: boolean;
}

function authHeaders(): Record<string, string> {
  if (storedBasic) return { Authorization: `Basic ${storedBasic}` };
  if (devToken) return { Authorization: `Bearer ${devToken}` };
  return {};
}

/** Orgs this account can reach (the shared default org always included). */
export async function fetchOrgs(): Promise<OrgInfo[]> {
  const res = await fetch(new URL("api/orgs", baseUrl), { headers: authHeaders() });
  if (!res.ok) return [];
  const d = (await res.json()) as { orgs?: OrgInfo[] };
  return d.orgs ?? [];
}

/** Create an org (you become its admin); throws the server's structured
 * message on rejection (name taken, creation disabled, ...). */
export async function createOrg(name: string): Promise<OrgInfo> {
  const res = await fetch(new URL("api/orgs", baseUrl), {
    method: "POST",
    headers: { ...authHeaders(), "Content-Type": "application/json" },
    body: JSON.stringify({ name }),
  });
  if (!res.ok) {
    let msg = `creating org failed (HTTP ${res.status})`;
    try {
      const e = (await res.json()) as { Message?: string; Suggestion?: string };
      if (e.Message) msg = e.Message + (e.Suggestion ? ` — ${e.Suggestion}` : "");
    } catch {
      // plain-text body; keep the status message
    }
    throw new Error(msg);
  }
  return (await res.json()) as OrgInfo;
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

export interface OrgSettings {
  description?: string;
  global_required_checks?: string[];
}

export interface OrgMember {
  name: string;
  role: string;
}

export async function fetchOrgSettings(org: string): Promise<OrgSettings> {
  const res = await fetch(new URL(`api/orgs/${org}/settings`, baseUrl), { headers: authHeaders() });
  if (!res.ok) await throwStructured(res, "loading settings failed");
  const d = (await res.json()) as { settings?: OrgSettings };
  return d.settings ?? {};
}

export async function updateOrgSettings(org: string, settings: OrgSettings): Promise<OrgSettings> {
  const res = await fetch(new URL(`api/orgs/${org}/settings`, baseUrl), {
    method: "PUT",
    headers: { ...authHeaders(), "Content-Type": "application/json" },
    body: JSON.stringify(settings),
  });
  if (!res.ok) await throwStructured(res, "saving settings failed");
  const d = (await res.json()) as { settings?: OrgSettings };
  return d.settings ?? {};
}

export async function fetchOrgMembers(org: string): Promise<OrgMember[]> {
  const res = await fetch(new URL(`api/orgs/${org}/members`, baseUrl), { headers: authHeaders() });
  if (!res.ok) await throwStructured(res, "loading members failed");
  const d = (await res.json()) as { members?: OrgMember[] };
  return d.members ?? [];
}

export async function addOrgMember(org: string, name: string, role: string): Promise<void> {
  const res = await fetch(new URL(`api/orgs/${org}/members`, baseUrl), {
    method: "POST",
    headers: { ...authHeaders(), "Content-Type": "application/json" },
    body: JSON.stringify({ name, role }),
  });
  if (!res.ok) await throwStructured(res, "adding member failed");
}

export async function removeOrgMember(org: string, name: string): Promise<void> {
  const res = await fetch(new URL(`api/orgs/${org}/members/${name}`, baseUrl), {
    method: "DELETE",
    headers: authHeaders(),
  });
  if (!res.ok) await throwStructured(res, "removing member failed");
}

/** Switch this browser to an org ("" = the default org) and reload so
 * every client rebinds its transport. */
export function switchOrg(name: string): void {
  if (name) window.localStorage.setItem("runko-org", name);
  else window.localStorage.removeItem("runko-org");
  window.location.reload();
}

const transport = usingDemoData
  ? createFakeTransport()
  : createConnectTransport({ baseUrl: transportBase!, interceptors: [auth] });

/** Validate name+password against runkod (GET /api/whoami) and, on
 * success, store the Basic credential for this browser and reload so
 * every client picks it up. Throws with a human-readable message on
 * rejection. */
export async function signIn(name: string, password: string): Promise<void> {
  const basic = btoa(`${name}:${password}`);
  const res = await fetch(new URL("api/whoami", baseUrl), {
    headers: { Authorization: `Basic ${basic}` },
  });
  if (res.status === 401) throw new Error("wrong name or password");
  if (!res.ok) throw new Error(`runkod answered HTTP ${res.status}`);
  const who = (await res.json()) as { name?: string; anonymous?: boolean };
  // A deploy-token password signs in "anonymous" - allowed (it is the
  // documented everyone-credential until retired) but shown as such.
  window.localStorage.setItem("runko-user", who.anonymous ? "" : (who.name ?? name));
  window.localStorage.setItem("runko-basic", basic);
  window.location.reload();
}

/** Whether this control plane offers self-service sign-up (§15.1),
 * fetched unauthenticated so the login page can decide what to render. */
export interface AuthConfig {
  signupEnabled: boolean;
  codeRequired: boolean;
  orgCreateEnabled: boolean;
}

export async function fetchAuthConfig(): Promise<AuthConfig> {
  const none = { signupEnabled: false, codeRequired: false, orgCreateEnabled: false };
  try {
    const res = await fetch(new URL("api/auth/config", baseUrl));
    if (!res.ok) return none;
    const d = (await res.json()) as {
      signup_enabled?: boolean;
      code_required?: boolean;
      org_create_enabled?: boolean;
    };
    return {
      signupEnabled: !!d.signup_enabled,
      codeRequired: !!d.code_required,
      orgCreateEnabled: !!d.org_create_enabled,
    };
  } catch {
    return none;
  }
}

/** Create a principal (and, when org is non-empty, their org - the
 * standard account+workspace sign-up shape) via POST /api/signup, then
 * sign in. The browser lands directly inside the new org. Throws with the
 * server's structured message + suggestion on rejection. */
export async function signUp(name: string, password: string, code: string, org: string): Promise<void> {
  const res = await fetch(new URL("api/signup", baseUrl), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, password, code, org }),
  });
  if (!res.ok) await throwStructured(res, "sign-up failed");
  if (org) window.localStorage.setItem("runko-org", org);
  await signIn(name, password);
}

/** Clear this browser's credential and reload. */
export function signOut(): void {
  window.localStorage.removeItem("runko-user");
  window.localStorage.removeItem("runko-basic");
  window.location.reload();
}

export const changesClient = createClient(ChangeService, transport);
export const projectsClient = createClient(ProjectService, transport);
export const repoClient = createClient(RepoService, transport);
export const workspacesClient = createClient(WorkspaceService, transport);
export const searchClient = createClient(SearchService, transport);
