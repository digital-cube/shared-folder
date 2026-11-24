// shfd-gui: the web GUI of shfd (docs/gui.md).
//
//	SHFD_URL          shfd's HTTP API (default http://127.0.0.1:8443)
//	SHFD_GUI_LISTEN   host:port to listen on (default 127.0.0.1:8444)
//	SHFD_GUI_BASE     base path: /gui (the default), or empty (or /) for the
//	                  root of the host; the same build serves either
//	SHFD_GUI_WEB      the built web app (default: web/ next to this file)
//	SHFD_GUI_COOKIE_SECURE=0   drop Secure from the cookie (plain-HTTP development only)

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { buildGui } from "./app.js";

const listen = process.env.SHFD_GUI_LISTEN ?? "127.0.0.1:8444";
const i = listen.lastIndexOf(":");
const host = i > 0 ? listen.slice(0, i).replace(/^\[|\]$/g, "") : "127.0.0.1";
const port = Number(i >= 0 ? listen.slice(i + 1) : listen);

const here = dirname(fileURLToPath(import.meta.url));
let version = "dev";
try {
  version = readFileSync(join(here, "VERSION"), "utf8").trim() || version;
} catch {
  // not a built gui/dist
}
const { app } = await buildGui({
  shfdUrl: process.env.SHFD_URL ?? "http://127.0.0.1:8443",
  base: process.env.SHFD_GUI_BASE ?? "/gui",
  version,
  webDir: process.env.SHFD_GUI_WEB ?? join(here, "web"),
  secureCookie: process.env.SHFD_GUI_COOKIE_SECURE !== "0",
  logger: true,
});

for (const sig of ["SIGINT", "SIGTERM"] as const) {
  process.once(sig, () => {
    app.close().then(() => process.exit(0));
  });
}
await app.listen({ host, port });
