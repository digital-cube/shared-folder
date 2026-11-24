// The GUI's own endpoints (sign-in, the folder choice, the account);
// everything of a folder goes through jsudc.

import { BASE_META, normalizeBase } from "../../shared/base";
import { getLang, LANG_HEADER, tr } from "./lang";

/** The session as the GUI server shows it (never the token). */
export interface Me {
  /** "account": a password sign-in; "token": the hidden sign-in with a member token. */
  kind: "account" | "token";
  username: string;
  display_name: string;
  must_change: boolean;
  /** The chosen folder; "" while an account has not chosen one. */
  share_id: string;
  share_name: string;
  /** The role in the chosen folder: "admin", "member", or "" without one. */
  role: string;
  /** The member's name in that folder. */
  member: string;
  /** The account's picture as shfd gives it (shared/avatar.ts avatarSrc); null without one. */
  avatar_url?: string | null;
}

export interface Folder {
  share_id: string;
  name: string;
  role: string;
  member: string;
  last_change_at?: string | null;
}

export interface Device {
  id: number;
  name: string;
  kind: string;
  created_at: string;
  last_seen_at?: string | null;
  expires_at?: string | null;
  current?: boolean;
}

export interface PasswordRules {
  min_len: number;
  max_len: number;
  not_username: boolean;
  differ_from_old?: boolean;
}

export interface Account {
  user: {
    username: string;
    display_name: string;
    email?: string;
    must_change: boolean;
    created_at: string;
    last_login_at?: string | null;
    avatar_url?: string | null;
  };
  folders: Folder[];
  devices: Device[];
  password_rules?: PasswordRules;
  session: Me;
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

/** An answer of the GUI server: the body, or the sentence of its refusal. */
export type Answer<T> = { ok: true; data: T } | { ok: false; status: number; error: string; code?: string };

/** A call of the GUI server in the UI's language; a refusal comes back as its sentence. */
export async function api<T>(method: string, path: string, body?: unknown, f: typeof fetch = fetch): Promise<Answer<T>> {
  let r: Response;
  try {
    r = await f(guiUrl(path), {
      method,
      credentials: "same-origin",
      headers: { [LANG_HEADER]: getLang(), ...(body === undefined ? {} : { "content-type": "application/json" }) },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch {
    return { ok: false, status: 0, error: tr("GUI server ne odgovara.", "The GUI server is not answering.") };
  }
  const data = await r.json().catch(() => ({}));
  if (!r.ok) return { ok: false, status: r.status, error: data.error ?? tr("Nešto nije uspelo.", "Something went wrong."), code: data.code };
  return { ok: true, data: data as T };
}

export async function getSession(): Promise<Me | null> {
  const r = await fetch(guiUrl("/api/session"), { credentials: "same-origin" });
  if (r.status === 401) return null;
  if (!r.ok) throw new Error(`session: ${r.status}`);
  return r.json();
}

/** Whether the hidden "Prijava tokenom" is on (SHFD_GUI_TOKEN_LOGIN). */
export async function getConfig(): Promise<{ token_login: boolean }> {
  const r = await api<{ token_login: boolean }>("GET", "/api/config");
  return r.ok ? r.data : { token_login: false };
}

export const signIn = (username: string, password: string) => api<Me>("POST", "/api/auth/login", { username, password });
export const signInWithToken = (token: string) => api<Me>("POST", "/api/login", { token });
export const getAccount = () => api<Account>("GET", "/api/me");
export const chooseFolder = (shareId: string) => api<Me>("POST", "/api/folder", { share_id: shareId });
export const changePassword = (oldPw: string, newPw: string) => api<Me>("POST", "/api/me/password", { old: oldPw, new: newPw });
export const revokeDevice = (id: number) => api<{ ok: boolean; signed_out: boolean }>("DELETE", `/api/me/devices/${id}`);
/** A forgotten password: the answer is the same whether the account exists or not. */
export const forgotPassword = (login: string, f?: typeof fetch) => api<{ ok: boolean; message: string }>("POST", "/api/auth/forgot", { login }, f);
/** A new password with the token of the mailed link. */
export const resetPassword = (token: string, newPassword: string, f?: typeof fetch) =>
  api<{ ok: boolean; username: string }>("POST", "/api/auth/reset", { token, new_password: newPassword }, f);
export const setMyEmail = (email: string, currentPassword: string, f?: typeof fetch) =>
  api<{ ok: boolean; email: string }>("PUT", "/api/me/email", { email, current_password: currentPassword }, f);

/** The answer to a new or removed picture: the session with its new avatar_url. */
export interface AvatarAnswer {
  ok: boolean;
  avatar_url: string | null;
  session: Me;
}

/** Sends the chosen file as it is; shfd reads the format from the content and makes the square. */
export async function uploadAvatar(file: Blob, f: typeof fetch = fetch): Promise<Answer<AvatarAnswer>> {
  let r: Response;
  try {
    r = await f(guiUrl("/api/me/avatar"), {
      method: "PUT",
      credentials: "same-origin",
      headers: { [LANG_HEADER]: getLang(), "content-type": file.type || "application/octet-stream" },
      body: file,
    });
  } catch {
    return { ok: false, status: 0, error: tr("GUI server ne odgovara.", "The GUI server is not answering.") };
  }
  const data = await r.json().catch(() => ({}));
  if (!r.ok) return { ok: false, status: r.status, error: data.error ?? tr("Nešto nije uspelo.", "Something went wrong."), code: data.code };
  return { ok: true, data: data as AvatarAnswer };
}

export const removeAvatar = (f?: typeof fetch) => api<AvatarAnswer>("DELETE", "/api/me/avatar", undefined, f);

export async function logout(): Promise<void> {
  await api("POST", "/api/logout");
}
