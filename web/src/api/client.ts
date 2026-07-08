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

// The bearer token is per-BROWSER at runtime (localStorage, set from the
// sidebar), falling back to the build-time var for local dev only. Never
// bake VITE_RUNKO_TOKEN into a published image: Vite inlines it into the
// bundle, which would hand the deploy token to every visitor.
const token: string | undefined =
  (typeof window !== "undefined" ? window.localStorage.getItem("runko-token") : null) ??
  import.meta.env.VITE_RUNKO_TOKEN;

/** True when the current page lives under the /demo mount (see main.tsx). */
export const onDemoRoute =
  typeof window !== "undefined" &&
  (window.location.pathname === "/demo" || window.location.pathname.startsWith("/demo/"));

export const usingDemoData = onDemoRoute || !baseUrl;

/** Live transport configured but no token in this browser yet: every RPC
 * will 401 until the user sets one (Layout surfaces this). */
export const liveTokenMissing = !usingDemoData && !token;

const auth: Interceptor = (next) => (req) => {
  if (token) req.header.set("Authorization", `Bearer ${token}`);
  return next(req);
};

const transport = usingDemoData
  ? createFakeTransport()
  : createConnectTransport({ baseUrl: baseUrl!, interceptors: [auth] });

/** Store (or clear) the runkod deploy token for this browser and reload so
 * every client picks it up. */
export function setRunkoToken(value: string): void {
  if (value) window.localStorage.setItem("runko-token", value);
  else window.localStorage.removeItem("runko-token");
  window.location.reload();
}

export const changesClient = createClient(ChangeService, transport);
export const projectsClient = createClient(ProjectService, transport);
export const repoClient = createClient(RepoService, transport);
export const workspacesClient = createClient(WorkspaceService, transport);
export const searchClient = createClient(SearchService, transport);
