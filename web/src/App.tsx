import { Navigate, Route, Routes } from "react-router-dom";
import { Layout } from "./components/Layout";
import { BrowsePage } from "./pages/BrowsePage";
import { ChangePage } from "./pages/ChangePage";
import { ChangesPage } from "./pages/ChangesPage";
import { GraphPage } from "./pages/GraphPage";
import { ProjectPage } from "./pages/ProjectPage";
import { ProjectsPage } from "./pages/ProjectsPage";
import { SearchPage } from "./pages/SearchPage";
import { WorkspacesPage } from "./pages/WorkspacesPage";

export default function App() {
  return (
    <Routes>
      <Route element={<Layout />}>
        <Route index element={<Navigate to="/changes" replace />} />
        <Route path="/changes" element={<ChangesPage />} />
        <Route path="/changes/:changeId" element={<ChangePage />} />
        {/* Splat: file paths contain slashes. */}
        <Route path="/browse/*" element={<BrowsePage />} />
        <Route path="/graph" element={<GraphPage />} />
        <Route path="/projects" element={<ProjectsPage />} />
        {/* Splat: project names contain slashes (commerce/cart). */}
        <Route path="/projects/*" element={<ProjectPage />} />
        <Route path="/workspaces" element={<WorkspacesPage />} />
        <Route path="/search" element={<SearchPage />} />
        <Route path="*" element={<Navigate to="/changes" replace />} />
      </Route>
    </Routes>
  );
}
