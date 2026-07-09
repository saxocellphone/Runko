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

/** Operator session (deploy token / flag principal): sees the deployment
 * admin panel. Display state - every admin endpoint re-checks. The
 * dev-token loop is an operator by construction. */
export const isOperator =
  !usingDemoData &&
  ((!!storedBasic && window.localStorage.getItem("runko-operator") === "1") ||
    (!storedBasic && !!devToken));

/** Live transport configured but no credential in this browser yet: every
 * RPC will 401, so App gates on the sign-in screen - unless the target
 * org is public_read (§15.2), in which case App enters anonymous
 * read-only browsing instead. */
export const liveUnauthenticated = !usingDemoData && !storedBasic && !devToken;

/** Anonymous read-only browsing of a public_read org (§15.2). Set by
 * App's boot probe BEFORE any page renders (App gates rendering on the
 * probe), so components may read it like the other module consts. */
export let publicBrowse = false;
export function markPublicBrowse(): void {
  publicBrowse = true;
}

/** Whether org ("" = the default org) answers anonymous reads - the
 * §15.2 public_read probe, against an allowlisted GET. */
export async function probePublicOrg(org: string): Promise<boolean> {
  try {
    const res = await fetch(new URL(org ? `o/${org}/api/projects` : "api/projects", baseUrl));
    return res.ok;
  } catch {
    return false;
  }
}

/** Navigate to org's own URL (/<org> - the shareable, GitHub-style entry;
 * a full navigation so the router remounts under the org basename). */
export function browsePublicOrg(name: string): void {
  window.location.href = `/${name}`;
}

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

// GitHub-style org path URLs: /<org>/... binds the whole app to that org
// (main.tsx mounts the router under the /<org> basename, the demo-mount
// pattern). The first path segment is an org exactly when it is org-shaped
// and not one of the app's own root routes - the server reserves those as
// org names (runkod/orghub.go reservedOrgNames), so this cannot be
// ambiguous for real orgs.
const appRootRoutes = new Set([
  "changes",
  "browse",
  "projects",
  "workspaces",
  "search",
  "settings",
  "admin",
  "graph",
  "login",
  "signup",
  "demo",
  "landing",
  "assets",
  "o",
  "api",
]);
const firstSegment =
  typeof window !== "undefined" ? (window.location.pathname.split("/")[1] ?? "") : "";
export const pathOrg =
  !onDemoRoute && /^[a-z][a-z0-9-]{0,38}$/.test(firstSegment) && !appRootRoutes.has(firstSegment)
    ? firstSegment
    : "";

export const currentOrg: string = pathOrg || (!usingDemoData && storedOrg ? storedOrg : "");

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
  /** §15.2 public_read: anonymous read-only access (git upload-pack, the
   * GET allowlist, read RPCs, and the read-only web UI at /<org>). */
  public_read?: boolean;
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

export interface AdminOrgRow {
  name: string;
  description: string;
  members: string[] | null;
  archived: boolean;
  default: boolean;
}

/** The deployment's whole org estate (operator-only; archived included). */
export async function fetchAdminOrgs(): Promise<AdminOrgRow[]> {
  const res = await fetch(new URL("api/admin/orgs", baseUrl), { headers: authHeaders() });
  if (!res.ok) await throwStructured(res, "loading the org estate failed");
  const d = (await res.json()) as { orgs?: AdminOrgRow[] };
  return d.orgs ?? [];
}

/** Archive/unarchive an org (operator-only). Archived orgs keep row +
 * repo; their whole surface answers 410 until unarchived. */
export async function setOrgArchived(org: string, archived: boolean): Promise<void> {
  const res = await fetch(new URL(`api/orgs/${org}/${archived ? "archive" : "unarchive"}`, baseUrl), {
    method: "POST",
    headers: authHeaders(),
  });
  if (!res.ok) await throwStructured(res, "changing archive state failed");
}

/** Switch this browser to another of YOUR orgs and reload so every client
 * rebinds its transport. Sessions are always org-scoped; the transport
 * runs through /o/<org>/ for every org, the default one included. */
export function switchOrg(name: string): void {
  window.localStorage.setItem("runko-org", name);
  window.location.reload();
}

const transport = usingDemoData
  ? createFakeTransport()
  : createConnectTransport({ baseUrl: transportBase!, interceptors: [auth] });

/** Org-scoped sign-in (2026-07-09: logging in means logging into AN ORG):
 * validate name+password against the ORG's own surface
 * (GET /o/<org>/api/whoami - membership is part of authentication there)
 * and, on success, bind this browser's session to that org and reload so
 * every client picks it up. Throws with a human-readable message on
 * rejection - including "valid account, wrong org". */
export async function signIn(name: string, password: string, org: string): Promise<void> {
  const basic = btoa(`${name}:${password}`);
  const res = await fetch(new URL(`o/${org}/api/whoami`, baseUrl), {
    headers: { Authorization: `Basic ${basic}` },
  });
  if (res.status === 401) throw new Error("wrong name or password");
  if (res.status === 403) throw new Error(`your account is not a member of org “${org}”`);
  if (res.status === 404) throw new Error(`no org named “${org}” here — check the spelling`);
  if (!res.ok) throw new Error(`runkod answered HTTP ${res.status}`);
  const who = (await res.json()) as { name?: string; anonymous?: boolean; operator?: boolean };
  // A deploy-token password signs in "anonymous" - allowed (it is the
  // operator credential) but shown as such. Operator-ness (server config,
  // not signup rows) gates the deployment admin panel; display state
  // only - the server re-checks on every admin call.
  window.localStorage.setItem("runko-user", who.anonymous ? "" : (who.name ?? name));
  window.localStorage.setItem("runko-basic", basic);
  window.localStorage.setItem("runko-org", org);
  window.localStorage.setItem("runko-operator", who.operator ? "1" : "0");
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

/** Create a principal via POST /api/signup - every account arrives INTO
 * an org, either creating one (orgMode "create": you become its admin) or
 * joining an existing one (orgMode "join": open to anyone for now; per-org
 * invites are the planned tightening). Signs in on success and lands the
 * browser inside that org. Throws the server's structured message on
 * rejection. */
export async function signUp(
  name: string,
  password: string,
  code: string,
  org: string,
  orgMode: "create" | "join",
): Promise<void> {
  const res = await fetch(new URL("api/signup", baseUrl), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, password, code, org, org_mode: orgMode }),
  });
  if (!res.ok) await throwStructured(res, "sign-up failed");
  const d = (await res.json()) as { org?: { name?: string } };
  // Land inside the chosen org - sign-in is org-scoped.
  await signIn(name, password, d.org?.name ?? org);
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
