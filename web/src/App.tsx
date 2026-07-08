import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { liveUnauthenticated } from "./api/client";
import { Layout } from "./components/Layout";
import { BrowsePage } from "./pages/BrowsePage";
import { ChangePage } from "./pages/ChangePage";
import { ChangesPage } from "./pages/ChangesPage";
import { LoginPage } from "./pages/LoginPage";
import { NewProjectPage } from "./pages/NewProjectPage";
import { ProjectPage } from "./pages/ProjectPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { SearchPage } from "./pages/SearchPage";
import { WorkspacesPage } from "./pages/WorkspacesPage";

export default function App() {
  // A live control plane with no credential in this browser: everything
  // would 401, so gate on sign-in instead of a wall of errors. The /demo
  // mount never reaches this (it runs the fake transport, and the login
  // page links to it for anonymous visitors).
  if (liveUnauthenticated) return <LoginPage />;
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
        <Route path="/projects/new" element={<NewProjectPage />} />
        {/* Splat: project names contain slashes (commerce/cart). */}
        <Route path="/projects/*" element={<ProjectPage />} />
        <Route path="/workspaces" element={<WorkspacesPage />} />
        <Route path="/search" element={<SearchPage />} />
        <Route path="*" element={<Navigate to="/changes" replace />} />
      </Route>
    </Routes>
  );
}

function GraphRedirect() {
  const location = useLocation();
  return <Navigate to={{ pathname: "/projects", search: location.search }} replace />;
}
