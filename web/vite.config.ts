import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  // Relative asset paths so the bundle works under any URL prefix.
  base: "./",
  build: {
    // Output directly into the Go embed directory.
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      // In dev mode, forward API calls to the running orchestrator.
      "/api": "http://localhost:7948",
    },
  },
});
