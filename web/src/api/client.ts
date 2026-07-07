// Service clients for the four runko.v1 services (proto/runko/v1/).
//
// Transport selection: set VITE_RUNKO_URL to a Connect-serving runkod to
// talk to a real control plane (Connect-Web speaks the Connect protocol
// over fetch; the Go side is expected to mount connect-go handlers per
// proto/README.md item 2). Without it, an in-memory fake transport serves
// the demo fixture scene - same generated types, same call sites.
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { ChangeService } from "../gen/runko/v1/changes_pb";
import { ProjectService } from "../gen/runko/v1/projects_pb";
import { SearchService } from "../gen/runko/v1/search_pb";
import { WorkspaceService } from "../gen/runko/v1/workspaces_pb";
import { createFakeTransport } from "./fake/transport";

const baseUrl: string | undefined = import.meta.env.VITE_RUNKO_URL;

export const usingDemoData = !baseUrl;

const transport = baseUrl ? createConnectTransport({ baseUrl }) : createFakeTransport();

export const changesClient = createClient(ChangeService, transport);
export const projectsClient = createClient(ProjectService, transport);
export const workspacesClient = createClient(WorkspaceService, transport);
export const searchClient = createClient(SearchService, transport);
