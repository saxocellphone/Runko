/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    // `npm run dev:lan` serves the working tree through the cluster
    // ingress at runko-dev.k8s.home (see k8s-cluster
    // apps/monorepo-platform/web-dev.yaml): a selectorless Service +
    // EndpointSlice point at this dev server on the LAN, so frontend
    // edits show up on save - no image build, no rollout. allowedHosts
    // whitelists exactly these hostnames (Vite blocks unknown Host
    // headers against DNS rebinding). The public tunnel host is an
    // explicit owner decision - see web-dev.yaml in k8s-cluster.
    allowedHosts: ["runko-dev.k8s.home", "runko-dev.victornazzaro.com"],
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts"],
  },
});
