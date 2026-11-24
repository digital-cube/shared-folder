// The base path of shfd-gui, the same on the server and in the browser:
// "" when the GUI is the root of its host, "/gui" (no trailing slash) under
// a prefix. One build serves either; SHFD_GUI_BASE picks it at run time and
// the server writes it into index.html (docs/gui.md).

/** The name of the <meta> that carries the base into the web app. */
export const BASE_META = "shfd-gui-base";

/** "", "/" and "/gui/" become "" and "/gui"; a missing leading slash is added. */
export function normalizeBase(raw: string | null | undefined): string {
  const s = (raw ?? "").trim().replace(/\/+$/, "");
  if (!s) return "";
  return s.startsWith("/") ? s : `/${s}`;
}

/**
 * The paths of shfd that the GUI, when it is the root of the host, must
 * neither serve nor hide: nginx sends them to shfd (gui/deploy/nginx-gui-root.conf).
 */
export function isShfdPath(path: string): boolean {
  const p = path.split("?")[0];
  return p === "/v1" || p.startsWith("/v1/") || p === "/healthz" || p === "/downloads" || p.startsWith("/downloads/");
}
