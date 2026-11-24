import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig } from "vite";

const root = fileURLToPath(new URL(".", import.meta.url));

// Everything lives under /gui. In development `npm run dev:web` proxies the
// API, the jsudc actions and the change stream to `npm run dev:server`, and
// the packages for download (shfd's own routes, no token) to shfd.
export default defineConfig({
  root,
  base: "/gui/",
  plugins: [react()],
  build: {
    outDir: fileURLToPath(new URL("../dist/web", import.meta.url)),
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
  },
  server: {
    port: 5174,
    proxy: {
      "/gui/api": "http://127.0.0.1:8444",
      "/gui/udc": "http://127.0.0.1:8444",
      "/v1/downloads": "http://127.0.0.1:8443",
      "/downloads/": "http://127.0.0.1:8443",
    },
  },
});
