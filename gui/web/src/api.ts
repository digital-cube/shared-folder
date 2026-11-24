// The GUI's own endpoints (sign-in); everything else goes through jsudc.

import { BASE_META, normalizeBase } from "../../shared/base";
import { getLang, LANG_HEADER, tr } from "./lang";

export interface Me {
  share_id: string;
  share_name: string;
  username: string;
  role: string;
}

/**
 * The base path the GUI is served under: "" at the root of the host, "/gui"
 * under a prefix. The server writes it into index.html as
 * <meta name="shfd-gui-base">; without one (a page served by something
 * else) it is the directory of the page.
 */
export function readBase(doc: Document | undefined = typeof document === "undefined" ? undefined : document): string {
  const meta = doc?.querySelector(`meta[name="${BASE_META}"]`)?.getAttribute("content");
  if (meta != null) return normalizeBase(meta);
  const path = doc?.location?.pathname ?? "/";
  return normalizeBase(path.slice(0, path.lastIndexOf("/") + 1));
}

export const BASE = readBase();

/** A URL of the GUI server: guiUrl("/api/session") is "/gui/api/session" under /gui. */
export function guiUrl(path: string, base: string = BASE): string {
  return `${base}${path}`;
}

export async function getSession(): Promise<Me | null> {
  const r = await fetch(guiUrl("/api/session"), { credentials: "same-origin" });
  if (r.status === 401) return null;
  if (!r.ok) throw new Error(`session: ${r.status}`);
  return r.json();
}

export async function login(token: string): Promise<{ ok: true; me: Me } | { ok: false; error: string }> {
  let r: Response;
  try {
    r = await fetch(guiUrl("/api/login"), {
      method: "POST",
      credentials: "same-origin",
      headers: { "content-type": "application/json", [LANG_HEADER]: getLang() },
      body: JSON.stringify({ token }),
    });
  } catch {
    return { ok: false, error: tr("GUI server ne odgovara.", "The GUI server is not answering.") };
  }
  const body = await r.json().catch(() => ({}));
  if (!r.ok) return { ok: false, error: body.error ?? tr("Prijava nije uspela.", "Sign-in failed.") };
  return { ok: true, me: body };
}

export async function logout(): Promise<void> {
  await fetch(guiUrl("/api/logout"), { method: "POST", credentials: "same-origin" });
}
