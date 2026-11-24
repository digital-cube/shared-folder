import { fileURLToPath } from "node:url";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";
import { BASE_META, normalizeBase } from "../shared/base";

const root = fileURLToPath(new URL(".", import.meta.url));

// One build for every base path: the assets are relative ("./") and the
// GUI server writes the base (SHFD_GUI_BASE, "" at the root of the host or
// "/gui") into index.html at run time (docs/gui.md).
//
// In development `npm run dev:web` serves the app at / with the base of
// `npm run dev:server` (the same SHFD_GUI_BASE, default /gui) and proxies
// the API, the jsudc actions and the change stream to it, and the packages
// for download (shfd's own routes, no token) to shfd.
const devBase = normalizeBase(process.env.SHFD_GUI_BASE ?? "/gui");

function devBaseMeta(): Plugin {
  return {
    name: "shfd-gui-dev-base",
    apply: "serve",
    transformIndexHtml: () => [{ tag: "meta", attrs: { name: BASE_META, content: devBase }, injectTo: "head-prepend" }],
  };
}

export default defineConfig({
  root,
  base: "./",
  plugins: [react(), devBaseMeta()],
  build: {
    outDir: fileURLToPath(new URL("../dist/web", import.meta.url)),
    emptyOutDir: true,
    chunkSizeWarningLimit: 2000,
  },
  server: {
    port: 5174,
    proxy: {
      [`${devBase}/api`]: "http://127.0.0.1:8444",
      [`${devBase}/udc`]: "http://127.0.0.1:8444",
      "/v1/downloads": "http://127.0.0.1:8443",
      "/downloads/": "http://127.0.0.1:8443",
    },
  },
});
