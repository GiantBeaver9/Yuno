import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Build output is written into ../web/dist so Go's //go:embed all:dist picks it
// up (single self-contained binary). Dev proxies /api to the Go backend.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../web/dist",
    emptyOutDir: true,
  },
  server: {
    proxy: {
      "/api": "http://localhost:8080",
    },
  },
});
