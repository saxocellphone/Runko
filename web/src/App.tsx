import { useEffect, useState } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import {
  currentOrg,
  liveUnauthenticated,
  markPublicBrowse,
  pathOrg,
  probePublicOrg,
  publicBrowse,
} from "./api/client";
import { Spinner } from "./components/ui";
import { Layout } from "./components/Layout";
import { BrowsePage } from "./pages/BrowsePage";
import { ChangePage } from "./pages/ChangePage";
import { ChangesPage } from "./pages/ChangesPage";
import { LoginPage } from "./pages/LoginPage";
import { NewProjectPage } from "./pages/NewProjectPage";
import { AdminPage } from "./pages/AdminPage";
import { OrgSettingsPage } from "./pages/OrgSettingsPage";
import { ProjectPage } from "./pages/ProjectPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { SearchPage } from "./pages/SearchPage";
import { WorkspacePage } from "./pages/WorkspacePage";
import { WorkspacesPage } from "./pages/WorkspacesPage";

export default function App() {
  // A live control plane with no credential in this browser: everything
  // would 401, so gate on sign-in instead of a wall of errors - unless
  // the org this browser points at is public_read (§15.2), in which case
  // browse it anonymously, read-only. The /demo mount never reaches this
  // (it runs the fake transport, and the login page links to it for
  // anonymous visitors).
  if (liveUnauthenticated) return <AnonGate />;
  return <AppRoutes />;
}

// AnonGate: decide between the sign-in page and anonymous read-only
// browsing. ?org=<name> deep links adopt that org (stored + reload, the
// switchOrg pattern); ?signin=1 forces the sign-in page (the read-only
// footer's Sign in button); ?invite=1 does too, landing in its
// "Request an invite" mode (the landing page's CTA, §15.1).
function AnonGate() {
  const params = new URLSearchParams(window.location.search);
  const wantSignin = params.has("signin") || params.has("invite");
  const urlOrg = params.get("org");
  const [mode, setMode] = useState<"probing" | "browse" | "login">(
    wantSignin ? "login" : "probing",
  );

  useEffect(() => {
    if (wantSignin) return;
    let stale = false;
    void (async () => {
      if (urlOrg && urlOrg !== currentOrg) {
        // Legacy ?org= links adopt the org's own URL (/<org> - the
        // GitHub-style entry main.tsx mounts a basename for).
        if (await probePublicOrg(urlOrg)) {
          window.location.href = `/${urlOrg}`;
          return;
        }
        if (!stale) setMode("login");
        return;
      }
      const ok = await probePublicOrg(currentOrg);
      if (stale) return;
      if (ok) markPublicBrowse();
      setMode(ok ? "browse" : "login");
    })();
    return () => {
      stale = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (mode === "probing") {
    return (
      <div className="app" style={{ display: "grid", placeItems: "center", minHeight: "100vh" }}>
        <Spinner />
      </div>
    );
  }
  if (mode === "login") return <LoginPage />;
  return <AppRoutes />;
}

function AppRoutes() {
  return (
    <Routes>
      <Route element={<Layout />}>
        {/* An org's own URL (/<org>) is its code page for everyone -
            GitHub semantics; the bare root app keeps the signed-in
            user's changes inbox as home. */}
        <Route
          index
          element={<Navigate to={pathOrg || publicBrowse ? "/browse" : "/changes"} replace />}
        />
        <Route path="/changes" element={<ChangesPage />} />
        <Route path="/changes/:changeId" element={<ChangePage />} />
        {/* Splat: file paths contain slashes. */}
        <Route path="/browse/*" element={<BrowsePage />} />
        {/* The DAG lives on the projects page now; keep old links working. */}
        <Route path="/graph" element={<GraphRedirect />} />
        <Route path="/projects" element={<ProjectsPage />} />
        <Route
          path="/projects/new"
          element={publicBrowse ? <Navigate to="/projects" replace /> : <NewProjectPage />}
        />
        {/* Splat: project names contain slashes (commerce/cart). */}
        <Route path="/projects/*" element={<ProjectPage />} />
        <Route path="/workspaces" element={<WorkspacesPage />} />
        {/* Workspace ids are single ref segments - no slashes, plain param. */}
        <Route path="/workspaces/:workspaceId" element={<WorkspacePage />} />
        <Route path="/search" element={<SearchPage />} />
        <Route path="/settings" element={<OrgSettingsPage />} />
        <Route path="/admin" element={<AdminPage />} />
        <Route path="*" element={<Navigate to="/changes" replace />} />
      </Route>
    </Routes>
  );
}

function GraphRedirect() {
  const location = useLocation();
  return <Navigate to={{ pathname: "/projects", search: location.search }} replace />;
}
