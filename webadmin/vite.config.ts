import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// The deployment admin panel serves under /admin behind the path-routed
// ingress (its own pod, separate from the main web app), so every asset
// URL is /admin/-prefixed at build time.
export default defineConfig({
  base: "/admin/",
  plugins: [react()],
});
