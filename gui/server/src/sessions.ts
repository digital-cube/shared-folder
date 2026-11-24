// Sessions of shfd-gui: a random id in an httpOnly cookie; the credential
// only here, in the memory of the process. A restart signs everyone out.
//
// A session is either an account's (kind "account": the GUI device token
// of a password sign-in, POST /v1/auth/login kind gui) or a member token's
// (kind "token": the hidden "Prijava tokenom", one folder). An account
// session picks its folder after the sign-in (shareId "" until then) and
// may switch it; its expiry follows shfd's for a GUI device: IDLE_MS
// without use, MAX_AGE_MS at most. shfd ends the token at the same moments,
// and a 401 from shfd ends the session sooner.

import { randomBytes } from "node:crypto";

export const COOKIE = "shfd_gui_sid";
/** shfd's GUIIdle: 12 h without use. */
export const IDLE_MS = 12 * 60 * 60 * 1000;
/** shfd's GUIMaxAge: 7 days from the sign-in, used or not. */
export const MAX_AGE_MS = 7 * 24 * 60 * 60 * 1000;

export type SessionKind = "account" | "token";

export interface Session {
  id: string;
  kind: SessionKind;
  /** The account's GUI device token, or the member token. */
  token: string;
  /** The account's username, or the member's for a token session. */
  username: string;
  displayName: string;
  /** The account has a temporary password: only the password change is open. */
  mustChange: boolean;
  /** The chosen folder; "" while an account has not chosen one. */
  shareId: string;
  shareName: string;
  /** The role in the chosen folder ("admin" | "member"), "" without one. */
  role: string;
  /** The member's name in the chosen folder. */
  member: string;
  /** The account's picture as shfd gives it (avatar_url); null without one. */
  avatarUrl: string | null;
  createdAt: number;
  lastUsed: number;
}

export interface NewSession {
  kind?: SessionKind;
  token: string;
  username: string;
  displayName?: string;
  mustChange?: boolean;
  shareId?: string;
  shareName?: string;
  role?: string;
  member?: string;
  avatarUrl?: string | null;
}

export class SessionStore {
  private readonly byId = new Map<string, Session>();

  constructor(
    private readonly now: () => number = Date.now,
    private readonly idleMs: number = IDLE_MS,
    private readonly maxAgeMs: number = MAX_AGE_MS,
  ) {}

  create(s: NewSession): Session {
    const id = randomBytes(32).toString("base64url");
    const t = this.now();
    const session: Session = {
      kind: s.kind ?? "token",
      token: s.token,
      username: s.username,
      displayName: s.displayName ?? "",
      mustChange: s.mustChange ?? false,
      shareId: s.shareId ?? "",
      shareName: s.shareName ?? "",
      role: s.role ?? "",
      member: s.member ?? s.username,
      avatarUrl: s.avatarUrl ?? null,
      id,
      createdAt: t,
      lastUsed: t,
    };
    this.byId.set(id, session);
    return session;
  }

  private expired(s: Session, now: number): boolean {
    if (now - s.lastUsed > this.idleMs) return true;
    return s.kind === "account" && now - s.createdAt > this.maxAgeMs;
  }

  /** The live session of id, refreshed; undefined when unknown or expired. */
  get(id: string | undefined): Session | undefined {
    if (!id) return undefined;
    const s = this.byId.get(id);
    if (!s) return undefined;
    const now = this.now();
    if (this.expired(s, now)) {
      this.byId.delete(id);
      return undefined;
    }
    s.lastUsed = now;
    return s;
  }

  delete(id: string | undefined): void {
    if (id) this.byId.delete(id);
  }

  /** Ends every session that uses token (shfd refused it: revoked, reset or expired). */
  dropToken(token: string): void {
    for (const [id, s] of this.byId) if (s.token === token) this.byId.delete(id);
  }

  /**
   * shfd answered 403 to token in a folder: a token session (admins only)
   * ends, an account session stays, as a member of that folder.
   */
  demote(token: string, shareId: string): void {
    for (const [id, s] of this.byId) {
      if (s.token !== token || s.shareId !== shareId) continue;
      if (s.kind === "token") this.byId.delete(id);
      else s.role = "member";
    }
  }

  /** Live sessions of a folder with the admin role, newest use first. */
  forShare(shareId: string): Session[] {
    this.sweep();
    return [...this.byId.values()]
      .filter((s) => s.shareId === shareId && s.role === "admin")
      .sort((a, b) => b.lastUsed - a.lastUsed);
  }

  /** Folders with at least one live admin session (whose logs the GUI follows). */
  shares(): string[] {
    this.sweep();
    return [...new Set([...this.byId.values()].filter((s) => s.shareId && s.role === "admin").map((s) => s.shareId))];
  }

  size(): number {
    this.sweep();
    return this.byId.size;
  }

  sweep(): void {
    const now = this.now();
    for (const [id, s] of this.byId) if (this.expired(s, now)) this.byId.delete(id);
  }
}

/** The value of one cookie in a Cookie header. */
export function readCookie(header: string | undefined, name: string): string | undefined {
  if (!header) return undefined;
  for (const part of header.split(";")) {
    const i = part.indexOf("=");
    if (i < 0) continue;
    if (part.slice(0, i).trim() === name) return decodeURIComponent(part.slice(i + 1).trim());
  }
  return undefined;
}

/** Set-Cookie for a session (maxAge 0 clears it). */
export function sessionCookie(value: string, opts: { path: string; secure: boolean; maxAgeSec?: number }): string {
  const parts = [`${COOKIE}=${encodeURIComponent(value)}`, `Path=${opts.path}`, "HttpOnly", "SameSite=Strict"];
  if (opts.secure) parts.push("Secure");
  if (opts.maxAgeSec !== undefined) parts.push(`Max-Age=${opts.maxAgeSec}`);
  return parts.join("; ");
}
