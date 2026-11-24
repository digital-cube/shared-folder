// Who sees which page, and which folder opens after a sign-in. Pure
// functions; the menu (Shell.tsx) and the app (App.tsx) follow them. The
// server holds the same line: shfd answers 403 to a member on the admin
// routes, and the GUI server refuses rename and remove to one.

import type { Folder, Me } from "./api";

export type Page = "members" | "logs" | "files" | "install" | "account" | "folders";
export const PAGES: Page[] = ["members", "logs", "files", "install", "account", "folders"];

export const isAdmin = (me: Pick<Me, "role">) => me.role === "admin";

/**
 * The pages of a session. An admin of the chosen folder has every page; a
 * member who is not one has Files (browse and download) and Moj nalog;
 * without a folder there are only the folder list and the account. A
 * token session has no account. Install is help, open to all.
 */
export function allowedPages(me: Pick<Me, "kind" | "role" | "share_id">): Page[] {
  const account: Page[] = me.kind === "account" ? ["folders", "account"] : [];
  if (!me.share_id) return [...account, "install"];
  if (isAdmin(me)) return ["members", "logs", "files", ...account, "install"];
  return ["files", ...account, "install"];
}

/** The page a session opens on: Članovi for an admin, Fajlovi for a member, the folder list without a folder. */
export function defaultPage(me: Pick<Me, "kind" | "role" | "share_id">): Page {
  if (!me.share_id) return me.kind === "account" ? "folders" : "install";
  return isAdmin(me) ? "members" : "files";
}

/** The page to show: the one asked for when the session may see it, else its default. */
export function pageFor(me: Pick<Me, "kind" | "role" | "share_id">, asked: Page): Page {
  return allowedPages(me).includes(asked) ? asked : defaultPage(me);
}

// ---- the folder choice, remembered per account in localStorage -----------

const KEY = (username: string) => `shfd-gui-folder:${username}`;

type Store = Pick<Storage, "getItem" | "setItem">;
const storage = (): Store | undefined => {
  try {
    return globalThis.localStorage ?? undefined;
  } catch {
    return undefined;
  }
};

export function rememberedFolder(username: string, s: Store | undefined = storage()): string | null {
  try {
    return s?.getItem(KEY(username)) ?? null;
  } catch {
    return null;
  }
}

export function rememberFolder(username: string, shareId: string, s: Store | undefined = storage()): void {
  try {
    s?.setItem(KEY(username), shareId);
  } catch {
    /* private mode, disabled storage */
  }
}

/**
 * The folder that opens by itself after a sign-in: the only one, or the
 * one this account chose last time in this browser while it is still
 * among its folders; otherwise none, and the list "Moji folderi" shows.
 */
export function autoFolder(folders: Pick<Folder, "share_id">[], remembered: string | null): string | null {
  if (folders.length === 1) return folders[0].share_id;
  if (remembered && folders.some((f) => f.share_id === remembered)) return remembered;
  return null;
}
