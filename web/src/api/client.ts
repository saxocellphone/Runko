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

const baseUrl: string | undefined = import.meta.env.VITE_RUNKO_URL;
const token: string | undefined = import.meta.env.VITE_RUNKO_TOKEN;

/** True when the current page lives under the /demo mount (see main.tsx). */
export const onDemoRoute =
  typeof window !== "undefined" &&
  (window.location.pathname === "/demo" || window.location.pathname.startsWith("/demo/"));

export const usingDemoData = onDemoRoute || !baseUrl;

const auth: Interceptor = (next) => (req) => {
  if (token) req.header.set("Authorization", `Bearer ${token}`);
  return next(req);
};

const transport = usingDemoData
  ? createFakeTransport()
  : createConnectTransport({ baseUrl: baseUrl!, interceptors: [auth] });

export const changesClient = createClient(ChangeService, transport);
export const projectsClient = createClient(ProjectService, transport);
export const repoClient = createClient(RepoService, transport);
export const workspacesClient = createClient(WorkspaceService, transport);
export const searchClient = createClient(SearchService, transport);
