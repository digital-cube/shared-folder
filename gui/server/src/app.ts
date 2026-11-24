// The shfd-gui server: sign-in, the jsudc collections and actions, the
// change feed and the built web app, all under one base path (/gui).

import { readFile, stat } from "node:fs/promises";
import { extname, join, normalize, sep } from "node:path";
import Fastify, { type FastifyInstance, type FastifyReply, type FastifyRequest } from "fastify";
import { UdcError, unauthenticatedError } from "@jsudc/core";
import { createChangeFeed, createDispatcher, udcPlugin } from "@jsudc/server";
import {
  addMember,
  overview,
  reactivateMember,
  resetToken,
  restoreTrash,
  revokeMember,
  setRole,
} from "../../shared/actions.js";
import { buildRegistry, type Ctx } from "./collections.js";
import { toUdcError } from "./errors.js";
import { FILE_OPS, LogFeed, summarize } from "./logs.js";
import { COOKIE, readCookie, sessionCookie, SessionStore } from "./sessions.js";
import { errorMessage, ShfdClient, ShfdError, type LogLine } from "./shfd.js";

export interface AppOptions {
  shfdUrl: string;
  base: string; // "/gui"
  webDir?: string; // the built web app; none in tests
  secureCookie: boolean;
  pollMs?: number;
  logger?: boolean;
  fetchFn?: typeof fetch;
  now?: () => number;
}

export const LOGIN_INVALID = "Token nije ispravan ili je opozvan.";
export const LOGIN_NOT_ADMIN = "Samo admin foldera može da koristi GUI.";

const TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".json": "application/json",
  ".woff2": "font/woff2",
  ".map": "application/json",
};

export interface GuiApp {
  app: FastifyInstance;
  sessions: SessionStore;
  feed: LogFeed;
}

export async function buildGui(o: AppOptions): Promise<GuiApp> {
  const base = o.base.replace(/\/+$/, "");
  const app = Fastify({ logger: o.logger ?? false, trustProxy: true });
  const shfd = new ShfdClient(o.shfdUrl, o.fetchFn);
  const sessions = new SessionStore(o.now);
  const changes = createChangeFeed();

  const notifyShare = async (shareId: string, fresh: LogLine[]) => {
    const ctx = { shareId } as Ctx;
    await changes.notify<Ctx>("logs", { op: "insert", ctx });
    await changes.notify<Ctx>("members", { op: "update", ctx });
    if (fresh.some((l) => l.type === "event" && (FILE_OPS.has(l.op) || l.op === "purge"))) {
      await changes.notify<Ctx>("files", { op: "update", ctx });
      await changes.notify<Ctx>("trash", { op: "update", ctx });
    }
  };
  const feed = new LogFeed({
    client: shfd,
    sessions,
    intervalMs: o.pollMs,
    onLines: notifyShare,
    log: app.log,
  });

  const registry = buildRegistry(shfd, feed);
  const dispatcher = createDispatcher<Ctx>(registry, {
    log: app.log,
    changes,
    rowScope: (ctx) => ({ field: "share_id", op: "eq", value: ctx.shareId }),
  });

  const cookieOpts = { path: `${base}/`, secure: o.secureCookie };
  const sessionOf = (req: FastifyRequest) => sessions.get(readCookie(req.headers.cookie, COOKIE));

  // A call of shfd for an action: a 401 means the token is dead, so the
  // session goes with it.
  const guard = async <T>(ctx: Ctx, f: () => Promise<T>): Promise<T> => {
    try {
      return await f();
    } catch (err) {
      if (err instanceof ShfdError && err.status === 401) sessions.dropToken(ctx.token);
      if (err instanceof ShfdError && err.status === 403) sessions.delete(ctx.sessionId);
      throw toUdcError(err);
    }
  };
  const membersChanged = (ctx: Ctx) => changes.notify<Ctx>("members", { op: "update", ctx });

  dispatcher.register(addMember, async ({ username, admin }, ctx: Ctx) => {
    const r = await guard(ctx, () => shfd.addMember(ctx.token, ctx.shareId, username.trim(), admin));
    await membersChanged(ctx);
    return { username: r.member.username, token: r.token, reactivated: !!r.reactivated };
  });
  dispatcher.register(reactivateMember, async ({ username }, ctx: Ctx) => {
    const list = await guard(ctx, () => shfd.members(ctx.token, ctx.shareId));
    const m = list.members.find((x) => x.username === username);
    if (!m) throw new UdcError("not_found", errorMessage("unknown_member"));
    if (m.status !== "revoked") throw new UdcError("validation_error", "Član nije opozvan.");
    const r = await guard(ctx, () => shfd.addMember(ctx.token, ctx.shareId, username, m.role === "admin"));
    await membersChanged(ctx);
    return { username, token: r.token, reactivated: !!r.reactivated };
  });
  dispatcher.register(revokeMember, async ({ username }, ctx: Ctx) => {
    const r = await guard(ctx, () => shfd.revoke(ctx.token, ctx.shareId, username));
    await membersChanged(ctx);
    if (username === ctx.username) sessions.delete(ctx.sessionId);
    return { username, status: r.member.status };
  });
  dispatcher.register(setRole, async ({ username, role }, ctx: Ctx) => {
    const r = await guard(ctx, () => shfd.setRole(ctx.token, ctx.shareId, username, role));
    await membersChanged(ctx);
    // An admin who gave up the role can no longer use the GUI.
    if (username === ctx.username && role !== "admin") sessions.delete(ctx.sessionId);
    return { username, role: r.member.role, old_role: r.old_role };
  });
  dispatcher.register(resetToken, async ({ username }, ctx: Ctx) => {
    const r = await guard(ctx, () => shfd.resetToken(ctx.token, ctx.shareId, username));
    await membersChanged(ctx);
    if (username === ctx.username) sessions.dropToken(ctx.token);
    return { username, token: r.token };
  });
  dispatcher.register(restoreTrash, async ({ id }, ctx: Ctx) => {
    const n = Number(id);
    if (!Number.isInteger(n) || n <= 0) throw new UdcError("validation_error", "Neispravan unos trash-a.");
    const r = await guard(ctx, () => shfd.restore(ctx.token, ctx.shareId, n));
    await changes.notify<Ctx>("trash", { op: "update", ctx });
    await changes.notify<Ctx>("files", { op: "update", ctx });
    let message: string;
    if (r.status === "unchanged") message = `Fajl ${r.path} već ima ovaj sadržaj; ništa nije menjano.`;
    else if (r.renamed) message = `Putanja je zauzeta drugim sadržajem, pa je fajl vraćen kao ${r.path}.`;
    else message = `Vraćeno u ${r.path}.`;
    return { path: r.path, status: r.status, renamed: !!r.renamed, message };
  });
  dispatcher.register(overview, async (_p, ctx: Ctx) => {
    const [st, health, members, lines] = await Promise.all([
      guard(ctx, () => shfd.stats(ctx.token, ctx.shareId)),
      shfd.health().catch(() => ({ service: "shfd", version: "?", ok: false })),
      guard(ctx, () => shfd.members(ctx.token, ctx.shareId)),
      guard(ctx, () => feed.lines(ctx.shareId, ctx.token)),
    ]);
    const active = members.members.filter((m) => m.status !== "revoked");
    const connected = members.members.some((m) => typeof m.connected === "number")
      ? members.members.reduce((n, m) => n + (m.connected ?? 0), 0)
      : null;
    return {
      version: health.version,
      server_ok: health.ok,
      head: st.head,
      files: st.files,
      files_size: st.files_size,
      trash: st.trash,
      trash_size: st.trash_size,
      members: active.length,
      connected,
      log: summarize(lines, new Date(o.now?.() ?? Date.now())),
    };
  });

  await app.register(udcPlugin, {
    prefix: base,
    dispatcher,
    registry,
    swagger: false,
    context: (req: FastifyRequest): Ctx => {
      const s = sessionOf(req);
      if (!s) throw unauthenticatedError("Prijava je istekla.");
      return { sessionId: s.id, token: s.token, shareId: s.shareId, username: s.username };
    },
  });

  // ---- sign-in -----------------------------------------------------------

  app.post(`${base}/api/login`, async (req, reply) => {
    const token = String((req.body as { token?: unknown } | undefined)?.token ?? "").trim();
    if (!token) return reply.code(400).send({ error: LOGIN_INVALID });
    let who;
    try {
      who = await shfd.whoami(token);
    } catch (err) {
      if (err instanceof ShfdError && err.status === 401) return reply.code(401).send({ error: LOGIN_INVALID });
      req.log.warn({ err: (err as Error).message }, "whoami failed");
      return reply.code(502).send({ error: errorMessage("unreachable") });
    }
    if (who.role !== "admin") return reply.code(403).send({ error: LOGIN_NOT_ADMIN });
    const s = sessions.create({
      token,
      shareId: who.share_id,
      shareName: who.share_name,
      username: who.username,
      role: who.role,
    });
    reply.header("set-cookie", sessionCookie(s.id, cookieOpts));
    return publicSession(s);
  });

  app.post(`${base}/api/logout`, async (req, reply) => {
    sessions.delete(readCookie(req.headers.cookie, COOKIE));
    reply.header("set-cookie", sessionCookie("", { ...cookieOpts, maxAgeSec: 0 }));
    return { ok: true };
  });

  app.get(`${base}/api/session`, async (req, reply) => {
    reply.header("cache-control", "no-store");
    const s = sessionOf(req);
    if (!s) return reply.code(401).send({ error: "not signed in" });
    return publicSession(s);
  });

  // ---- the web app -------------------------------------------------------

  app.get(base, async (_req, reply) => reply.redirect(`${base}/`));
  if (o.webDir) {
    const root = normalize(o.webDir);
    const serve = async (req: FastifyRequest, reply: FastifyReply) => {
      const rel = decodeURIComponent((req.params as { "*": string })["*"] ?? "");
      let file = normalize(join(root, rel));
      if (file !== root && !file.startsWith(root + sep)) return reply.code(404).send();
      const st = await stat(file).catch(() => undefined);
      if (!st || st.isDirectory()) file = join(root, "index.html");
      const body = await readFile(file);
      const ext = extname(file);
      reply.header("content-type", TYPES[ext] ?? "application/octet-stream");
      reply.header("x-content-type-options", "nosniff");
      if (ext === ".html") {
        reply.header("cache-control", "no-cache");
        reply.header("x-frame-options", "DENY");
        reply.header("referrer-policy", "same-origin");
      } else if (rel.startsWith("assets/")) {
        reply.header("cache-control", "public, max-age=31536000, immutable");
      }
      return reply.send(body);
    };
    app.get(`${base}/*`, serve);
  }

  app.addHook("onClose", async () => {
    feed.stop();
  });
  feed.start();
  return { app, sessions, feed };
}

function publicSession(s: { shareId: string; shareName: string; username: string; role: string }) {
  return { share_id: s.shareId, share_name: s.shareName, username: s.username, role: s.role };
}
