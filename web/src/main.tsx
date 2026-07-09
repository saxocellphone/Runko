import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";
import App from "./App";
import { onDemoRoute, pathOrg } from "./api/client";
import "./styles/global.css";

// Resolve the theme before first paint to avoid a flash of the wrong one.
const stored = localStorage.getItem("runko-theme");
document.documentElement.dataset.theme =
  stored === "dark" || stored === "light"
    ? stored
    : window.matchMedia("(prefers-color-scheme: dark)").matches
      ? "dark"
      : "light";

// The demo scene mounts under /demo with the fake transport; orgs get
// GitHub-style path URLs (/<org>/browse, ... - api/client.ts's pathOrg)
// mounting the same app under the org basename; the bare root app talks
// to the browser's stored org. Basename is decided at page load, so every
// in-app link stays inside its own world - crossing between them is a
// full navigation, deliberately.
createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <BrowserRouter basename={onDemoRoute ? "/demo" : pathOrg ? `/${pathOrg}` : undefined}>
      <App />
    </BrowserRouter>
  </StrictMode>,
);
