// Sessions of shfd-gui: a random id in an httpOnly cookie, the member's
// token only here, in the memory of the process. A restart signs everyone
// out; a session unused for IDLE_MS expires.

import { randomBytes } from "node:crypto";

export const COOKIE = "shfd_gui_sid";
export const IDLE_MS = 12 * 60 * 60 * 1000;

export interface Session {
  id: string;
  token: string;
  shareId: string;
  shareName: string;
  username: string;
  role: string;
  lastUsed: number;
}

export type NewSession = Omit<Session, "id" | "lastUsed">;

export class SessionStore {
  private readonly byId = new Map<string, Session>();

  constructor(
    private readonly now: () => number = Date.now,
    private readonly idleMs: number = IDLE_MS,
  ) {}

  create(s: NewSession): Session {
    const id = randomBytes(32).toString("base64url");
    const session: Session = { ...s, id, lastUsed: this.now() };
    this.byId.set(id, session);
    return session;
  }

  /** The live session of id, refreshed; undefined when unknown or expired. */
  get(id: string | undefined): Session | undefined {
    if (!id) return undefined;
    const s = this.byId.get(id);
    if (!s) return undefined;
    const now = this.now();
    if (now - s.lastUsed > this.idleMs) {
      this.byId.delete(id);
      return undefined;
    }
    s.lastUsed = now;
    return s;
  }

  delete(id: string | undefined): void {
    if (id) this.byId.delete(id);
  }

  /** Ends every session that uses token (shfd refused it: revoked or reset). */
  dropToken(token: string): void {
    for (const [id, s] of this.byId) if (s.token === token) this.byId.delete(id);
  }

  /** Live sessions of a folder, newest use first. */
  forShare(shareId: string): Session[] {
    this.sweep();
    return [...this.byId.values()]
      .filter((s) => s.shareId === shareId)
      .sort((a, b) => b.lastUsed - a.lastUsed);
  }

  /** Folders with at least one live session. */
  shares(): string[] {
    this.sweep();
    return [...new Set([...this.byId.values()].map((s) => s.shareId))];
  }

  size(): number {
    this.sweep();
    return this.byId.size;
  }

  sweep(): void {
    const now = this.now();
    for (const [id, s] of this.byId) if (now - s.lastUsed > this.idleMs) this.byId.delete(id);
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
