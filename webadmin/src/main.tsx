import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import "./styles.css";

// Resolve the theme before first paint to avoid a flash of the wrong
// one. Same key as the main web app - same origin, one theme choice.
const stored = localStorage.getItem("runko-theme");
document.documentElement.dataset.theme =
  stored === "dark" || stored === "light"
    ? stored
    : window.matchMedia("(prefers-color-scheme: dark)").matches
      ? "dark"
      : "light";

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
