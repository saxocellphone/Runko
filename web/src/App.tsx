import { useEffect, useState } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import {
  currentOrg,
  liveUnauthenticated,
  markPublicBrowse,
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
// footer's Sign in button).
function AnonGate() {
  const params = new URLSearchParams(window.location.search);
  const wantSignin = params.has("signin");
  const urlOrg = params.get("org");
  const [mode, setMode] = useState<"probing" | "browse" | "login">(
    wantSignin ? "login" : "probing",
  );

  useEffect(() => {
    if (wantSignin) return;
    let stale = false;
    void (async () => {
      if (urlOrg && urlOrg !== currentOrg) {
        if (await probePublicOrg(urlOrg)) {
          window.localStorage.setItem("runko-org", urlOrg);
          window.location.reload();
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
        <Route index element={<Navigate to="/changes" replace />} />
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
