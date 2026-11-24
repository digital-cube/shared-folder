// service.log as the GUI sees it: a buffer of parsed lines per folder, fed
// by polling shfd's admin/log once a second with the token of a signed-in
// admin of that folder, and the rows of the "logs" collection made from it.

import type { LogLine, ShfdClient } from "./shfd.js";
import { ShfdError } from "./shfd.js";
import type { SessionStore } from "./sessions.js";

/** How many lines the GUI keeps per folder (the newest). */
export const BUFFER_LINES = 5000;

/** What the logs table shows in the "Tip" column. */
export type LogKind = "izmena" | "transfer" | "kljuc" | "odbijeno" | "ostalo";

export interface LogRow {
  id: string; // byte offset in service.log
  ts: string;
  kind: LogKind;
  member: string;
  op: string;
  path: string;
  seq: number | null;
  outcome: string;
  detail: string;
  error: boolean; // outcome is not ok
  raw: string;
}

/** The ops of an event line that change what the Files page shows. */
export const FILE_OPS = new Set(["put", "delete", "move", "mkdir", "rmdir", "restore", "conflict"]);

export function kindOf(l: LogLine): LogKind {
  switch (l.type) {
    case "event":
      return "izmena";
    case "transfer":
      return l.outcome === "ok" ? "transfer" : "odbijeno";
    case "key-replaced":
      return "kljuc";
    default:
      return "ostalo";
  }
}

/** One row of the logs collection. */
export function toRow(l: LogLine): LogRow {
  let path = l.path ?? "";
  if (l.type === "event" && l.old_path) path = `${l.old_path} → ${l.path}`;
  if (l.type === "key-replaced") {
    const m = /old=(\S*) new=(\S*)/.exec(l.detail ?? "");
    path = m ? `${short(m[1])} → ${short(m[2])}` : (l.detail ?? "");
  }
  const outcome = l.outcome ?? "";
  return {
    id: String(l.offset),
    ts: l.ts || "",
    kind: kindOf(l),
    member: l.member ?? "",
    op: l.type === "transfer" && l.outcome !== "ok" ? l.outcome || l.op : l.op || (l.type === "other" ? "" : l.type),
    path,
    seq: l.seq ?? null,
    outcome,
    detail: l.type === "key-replaced" ? (l.detail ?? "") : (l.detail ?? ""),
    error: l.type === "transfer" && outcome !== "" && outcome !== "ok",
    raw: l.raw,
  };
}

/** A fingerprint cut for a table cell: "SHA256:Yp7c…02kd". */
export function short(fp: string): string {
  if (!fp) return "";
  const [alg, body] = fp.includes(":") ? [fp.slice(0, fp.indexOf(":") + 1), fp.slice(fp.indexOf(":") + 1)] : ["", fp];
  return body.length <= 10 ? fp : `${alg}${body.slice(0, 4)}…${body.slice(-4)}`;
}

interface ShareLog {
  lines: LogLine[];
  next: number;
  loaded: boolean;
  loading?: Promise<void>;
}

export interface LogFeedOptions {
  client: ShfdClient;
  sessions: SessionStore;
  /** Called with the new lines of a folder after a poll found some. */
  onLines: (shareId: string, lines: LogLine[]) => void | Promise<void>;
  intervalMs?: number;
  log?: { warn: (obj: object, msg: string) => void };
}

/** The log buffers and their poller. */
export class LogFeed {
  private readonly shares = new Map<string, ShareLog>();
  private timer?: NodeJS.Timeout;
  private ticking = false;

  constructor(private readonly o: LogFeedOptions) {}

  start(): void {
    this.timer ??= setInterval(() => void this.tick(), this.o.intervalMs ?? 1000);
    this.timer.unref?.();
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
  }

  /** The buffered lines of a folder, oldest first; loads them on first use. */
  async lines(shareId: string, token: string): Promise<LogLine[]> {
    const s = this.state(shareId);
    if (!s.loaded) await this.load(shareId, s, token);
    return s.lines;
  }

  private state(shareId: string): ShareLog {
    let s = this.shares.get(shareId);
    if (!s) {
      s = { lines: [], next: 0, loaded: false };
      this.shares.set(shareId, s);
    }
    return s;
  }

  private load(shareId: string, s: ShareLog, token: string): Promise<void> {
    s.loading ??= (async () => {
      try {
        const p = await this.o.client.log(token, shareId, undefined, BUFFER_LINES);
        s.lines = p.lines;
        s.next = p.next_offset;
        s.loaded = true;
      } finally {
        s.loading = undefined;
      }
    })();
    return s.loading;
  }

  /** One poll of every folder that has a signed-in admin. */
  async tick(): Promise<void> {
    if (this.ticking) return;
    this.ticking = true;
    try {
      const active = new Set(this.o.sessions.shares());
      for (const id of this.shares.keys()) if (!active.has(id)) this.shares.delete(id);
      for (const shareId of active) {
        const session = this.o.sessions.forShare(shareId)[0];
        if (!session) continue;
        const s = this.state(shareId);
        try {
          if (!s.loaded) {
            await this.load(shareId, s, session.token);
            continue;
          }
          await this.poll(shareId, s, session.token);
        } catch (err) {
          if (err instanceof ShfdError && (err.status === 401 || err.status === 403)) {
            // The token was revoked, reset or lost the admin role.
            this.o.sessions.dropToken(session.token);
          } else {
            this.o.log?.warn({ err, shareId }, "log poll failed");
          }
        }
      }
    } finally {
      this.ticking = false;
    }
  }

  private async poll(shareId: string, s: ShareLog, token: string): Promise<void> {
    const fresh: LogLine[] = [];
    for (let i = 0; i < 10; i++) {
      const p = await this.o.client.log(token, shareId, s.next, 1000);
      if (p.next_offset < s.next || (p.offset === 0 && s.next > p.size)) {
        // service.log was replaced: start over.
        s.loaded = false;
        await this.load(shareId, s, token);
        await this.o.onLines(shareId, s.lines);
        return;
      }
      fresh.push(...p.lines);
      s.next = p.next_offset;
      if (p.lines.length < 1000) break;
    }
    if (fresh.length === 0) return;
    s.lines = s.lines.concat(fresh);
    if (s.lines.length > BUFFER_LINES) s.lines = s.lines.slice(s.lines.length - BUFFER_LINES);
    await this.o.onLines(shareId, fresh);
  }
}

/** Numbers for the tiles above the logs table. */
export interface LogSummary {
  today: number;
  last_hour: number;
  transfers: number;
  uploads: number;
  downloads: number;
  refused: number;
  per_member: { member: string; count: number }[];
}

export function summarize(lines: LogLine[], now: Date): LogSummary {
  const dayStart = new Date(now);
  dayStart.setHours(0, 0, 0, 0);
  const hourAgo = now.getTime() - 3600_000;
  const out: LogSummary = { today: 0, last_hour: 0, transfers: 0, uploads: 0, downloads: 0, refused: 0, per_member: [] };
  const perMember = new Map<string, number>();
  for (const l of lines) {
    const t = Date.parse(l.ts);
    if (Number.isNaN(t) || t < dayStart.getTime()) continue;
    out.today++;
    if (l.type === "transfer") {
      if (l.outcome === "ok") {
        out.transfers++;
        if (l.dir === "upload") out.uploads++;
        if (l.dir === "download") out.downloads++;
      } else {
        out.refused++;
      }
    }
    if (t >= hourAgo) {
      out.last_hour++;
      if (l.member) perMember.set(l.member, (perMember.get(l.member) ?? 0) + 1);
    }
  }
  out.per_member = [...perMember].map(([member, count]) => ({ member, count })).sort((a, b) => b.count - a.count);
  return out;
}
