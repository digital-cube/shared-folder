// The shfd-gui server: sign-in, the jsudc collections and actions, the
// change feed and the built web app, all under one base path: "/gui", or ""
// when the GUI is the root of its host (SHFD_GUI_BASE, docs/gui.md). Its
// routes are <base>/api/…, <base>/udc/… and the web app; at the root it
// leaves shfd's paths (/v1/, /healthz, /downloads/) alone.

import { readFile, stat } from "node:fs/promises";
import { Readable } from "node:stream";
import { extname, join, normalize, sep } from "node:path";
import Fastify, { type FastifyInstance, type FastifyReply, type FastifyRequest } from "fastify";
import { UdcError, unauthenticatedError } from "@jsudc/core";
import { createChangeFeed, createDispatcher, udcPlugin } from "@jsudc/server";
import {
  addMember,
  overview,
  reactivateMember,
  removeFile,
  renameFile,
  resetToken,
  restoreTrash,
  revokeMember,
  setRole,
} from "../../shared/actions.js";
import { BASE_META, isShfdPath, normalizeBase } from "../../shared/base.js";
import { renameTarget } from "../../shared/rename.js";
import { buildRegistry, type Ctx } from "./collections.js";
import { toUdcError } from "./errors.js";
import { langOf, tr, type Lang } from "./lang.js";
import { FILE_OPS, LogFeed, summarize } from "./logs.js";
import { COOKIE, readCookie, sessionCookie, SessionStore } from "./sessions.js";
import { CONTENT_REQUEST_HEADERS, errorMessage, ShfdClient, ShfdError, type LogLine, type ThumbSize } from "./shfd.js";

export interface AppOptions {
  shfdUrl: string;
  base: string; // "/gui", or "" (or "/") for the root of the host
  version?: string; // shown by <base>/api/healthz
  webDir?: string; // the built web app; none in tests
  secureCookie: boolean;
  pollMs?: number;
  logger?: boolean;
  fetchFn?: typeof fetch;
  now?: () => number;
}

export const LOGIN_INVALID = "Token nije ispravan ili je opozvan.";
export const LOGIN_NOT_ADMIN = "Samo admin foldera može da koristi GUI.";
export const LOGIN_INVALID_EN = "The token is not valid or has been revoked.";
export const LOGIN_NOT_ADMIN_EN = "Only an admin of the folder can use the GUI.";

const loginInvalid = (lang: Lang) => tr(lang, LOGIN_INVALID, LOGIN_INVALID_EN);
const loginNotAdmin = (lang: Lang) => tr(lang, LOGIN_NOT_ADMIN, LOGIN_NOT_ADMIN_EN);

const THUMB_HASH = /^[0-9a-f]{64}$/;
const THUMB_SIZES = new Set(["s", "m", "l"]);

/** shfd's headers a file's content keeps on its way to the browser. */
export const CONTENT_RESPONSE_HEADERS = [
  "content-type",
  "content-disposition",
  "content-length",
  "content-range",
  "accept-ranges",
  "etag",
  "cache-control",
  "content-security-policy",
  "x-content-type-options",
] as const;

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
  const base = normalizeBase(o.base);
  const app = Fastify({ logger: o.logger ?? false, trustProxy: true, disableRequestLogging: true });
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
      throw toUdcError(err, ctx.lang);
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
    if (!m) throw new UdcError("not_found", errorMessage("unknown_member", undefined, ctx.lang));
    if (m.status !== "revoked") throw new UdcError("validation_error", tr(ctx.lang, "Član nije opozvan.", "The member is not revoked."));
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
    if (!Number.isInteger(n) || n <= 0) throw new UdcError("validation_error", tr(ctx.lang, "Neispravan unos trash-a.", "Invalid trash entry."));
    const r = await guard(ctx, () => shfd.restore(ctx.token, ctx.shareId, n));
    await changes.notify<Ctx>("trash", { op: "update", ctx });
    await changes.notify<Ctx>("files", { op: "update", ctx });
    const l = ctx.lang;
    let message: string;
    if (r.status === "unchanged")
      message = tr(l, `Fajl ${r.path} već ima ovaj sadržaj; ništa nije menjano.`, `The file ${r.path} already has this content; nothing was changed.`);
    else if (r.renamed)
      message = tr(l, `Putanja je zauzeta drugim sadržajem, pa je fajl vraćen kao ${r.path}.`, `The path holds other content, so the file was restored as ${r.path}.`);
    else message = tr(l, `Vraćeno u ${r.path}.`, `Restored to ${r.path}.`);
    return { path: r.path, status: r.status, renamed: !!r.renamed, message };
  });
  // Rename and remove are ops of the sync protocol, made with the admin's
  // token like a device would: shfd checks the version (base_hash), sends a
  // deleted file to the trash and tells every device.
  const filesChanged = async (ctx: Ctx) => {
    await changes.notify<Ctx>("files", { op: "update", ctx });
    await changes.notify<Ctx>("trash", { op: "update", ctx });
  };
  dispatcher.register(renameFile, async ({ path, name, base_hash }, ctx: Ctx) => {
    const to = renameTarget(path, name, ctx.lang);
    if (typeof to !== "string") throw new UdcError("validation_error", to.error);
    if (to === path) return { path, status: "unchanged", message: tr(ctx.lang, "Ime je isto; ništa nije menjano.", "The name is the same; nothing was changed.") };
    const r = await guard(ctx, () => shfd.op(ctx.token, ctx.shareId, { op: "move", path, new_path: to, base_hash }));
    await filesChanged(ctx);
    return { path: r.path, status: r.status, message: tr(ctx.lang, `Preimenovano u ${r.path.split("/").pop()}.`, `Renamed to ${r.path.split("/").pop()}.`) };
  });
  dispatcher.register(removeFile, async ({ path, kind, base_hash }, ctx: Ctx) => {
    const req = kind === "dir" ? ({ op: "rmdir", path } as const) : ({ op: "delete", path, base_hash } as const);
    const r = await guard(ctx, () => shfd.op(ctx.token, ctx.shareId, req));
    await filesChanged(ctx);
    const name = path.split("/").pop();
    const l = ctx.lang;
    let message: string;
    if (r.status === "unchanged") message = tr(l, `${name} već ne postoji.`, `${name} no longer exists.`);
    else if (kind === "dir") message = tr(l, `Folder ${name} je obrisan.`, `The folder ${name} was removed.`);
    else message = tr(l, `${name} je obrisan; može se vratiti iz Trash-a.`, `${name} was removed; it can be restored from the Trash.`);
    return { path: r.path, status: r.status, message };
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

  // At the root of the host the GUI shares the path space with shfd, whose
  // paths nginx sends to shfd; one that reaches the GUI anyway is not the
  // GUI's to answer (nor to hide behind index.html, nor jsudc's /healthz).
  if (base === "") {
    app.addHook("onRequest", async (req, reply) => {
      if (isShfdPath(req.url)) return reply.code(404).send({ error: "not found" });
    });
  }

  // The change stream must reach the browser unbuffered through every
  // reverse proxy on the way (nginx honours this header).
  app.addHook("onRequest", async (req, reply) => {
    if (req.url.startsWith(`${base}/udc/changes`)) reply.raw.setHeader("X-Accel-Buffering", "no");
  });

  await app.register(udcPlugin, {
    prefix: base,
    dispatcher,
    registry,
    swagger: false,
    context: (req: FastifyRequest): Ctx => {
      const s = sessionOf(req);
      const lang = langOf(req);
      if (!s) throw unauthenticatedError(tr(lang, "Prijava je istekla.", "The sign-in has expired."));
      return { sessionId: s.id, token: s.token, shareId: s.shareId, username: s.username, lang };
    },
  });

  // The GUI's health. jsudc also answers <base>/healthz, kept for the
  // checks written against /gui/healthz; at the root that path is shfd's.
  app.get(`${base}/api/healthz`, async (_req, reply) => {
    reply.header("cache-control", "no-store");
    return { service: "shfd-gui", version: o.version ?? "dev", ok: true };
  });

  // ---- sign-in -----------------------------------------------------------

  app.post(`${base}/api/login`, async (req, reply) => {
    const lang = langOf(req);
    const token = String((req.body as { token?: unknown } | undefined)?.token ?? "").trim();
    if (!token) return reply.code(400).send({ error: loginInvalid(lang) });
    let who;
    try {
      who = await shfd.whoami(token);
    } catch (err) {
      if (err instanceof ShfdError && err.status === 401) return reply.code(401).send({ error: loginInvalid(lang) });
      req.log.warn({ err: (err as Error).message }, "whoami failed");
      return reply.code(502).send({ error: errorMessage("unreachable", undefined, lang) });
    }
    if (who.role !== "admin") return reply.code(403).send({ error: loginNotAdmin(lang) });
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

  // ---- thumbnails --------------------------------------------------------

  // One thumbnail of the signed-in folder, from shfd with the session's
  // token: the bytes and the caching headers as shfd sent them, so the
  // browser keeps each one for good (the key is the content hash) and asks
  // again with If-None-Match at most.
  app.get(`${base}/api/thumb/:hash`, async (req, reply) => {
    const s = sessionOf(req);
    if (!s) return reply.code(401).send({ error: "not signed in" });
    const hash = (req.params as { hash: string }).hash;
    const size = String((req.query as { size?: unknown }).size ?? "m");
    if (!THUMB_HASH.test(hash)) return reply.code(400).send({ error: "invalid hash" });
    if (!THUMB_SIZES.has(size)) return reply.code(400).send({ error: "size must be s, m or l" });
    const inm = req.headers["if-none-match"];
    let r;
    try {
      r = await shfd.thumb(s.token, s.shareId, hash, size as ThumbSize, typeof inm === "string" ? inm : undefined);
    } catch (err) {
      if (err instanceof ShfdError && err.status === 401) {
        sessions.dropToken(s.token);
        return reply.code(401).send({ error: "not signed in" });
      }
      if (err instanceof ShfdError && err.status === 403) {
        sessions.delete(s.id);
        return reply.code(403).send({ error: loginNotAdmin(langOf(req)) });
      }
      req.log.warn({ err: (err as Error).message }, "thumbnail failed");
      return reply.code(502).send({ error: errorMessage("unreachable", undefined, langOf(req)) });
    }
    if (r.status !== 200 && r.status !== 304 && r.status !== 404) {
      return reply.code(502).send({ error: `shfd answered ${r.status}` });
    }
    reply.code(r.status);
    for (const h of ["etag", "cache-control"]) {
      const v = r.headers.get(h);
      if (v) reply.header(h, v);
    }
    if (r.status === 404) {
      reply.header("cache-control", "no-store");
      return reply.send({ error: "no thumbnail" });
    }
    if (r.status === 304) return reply.send();
    reply.header("content-type", r.headers.get("content-type") ?? "image/jpeg");
    reply.header("x-content-type-options", "nosniff");
    return reply.send(Buffer.from(r.body));
  });

  // ---- a file's content --------------------------------------------------

  // The content of one file of the signed-in folder, from shfd with the
  // session's token, streamed (never read whole) with shfd's headers: its
  // Content-Type and Content-Disposition (inline only for the safe types),
  // CSP sandbox, nosniff, ETag and ranges. Range, If-Range and
  // If-None-Match go to shfd, which answers them. ?download=1 always
  // downloads.
  app.get(`${base}/api/files/content`, async (req, reply) => {
    const s = sessionOf(req);
    if (!s) return reply.code(401).send({ error: "not signed in" });
    const q = req.query as { path?: unknown; download?: unknown };
    const path = typeof q.path === "string" ? q.path : "";
    if (!path) return reply.code(400).send({ error: "path is required" });
    const headers: Record<string, string> = {};
    for (const h of CONTENT_REQUEST_HEADERS) {
      const v = req.headers[h];
      if (typeof v === "string") headers[h] = v;
    }
    // The browser leaving ends the download from shfd too. (The response's
    // close, not the request's: a request closes once its body is read.)
    const ctl = new AbortController();
    const onClose = () => {
      if (!reply.raw.writableFinished) ctl.abort();
    };
    reply.raw.once("close", onClose);
    let r: Response;
    try {
      r = await shfd.content(s.token, s.shareId, path, { download: q.download === "1", headers, signal: ctl.signal });
    } catch (err) {
      reply.raw.off("close", onClose);
      reply.header("cache-control", "no-store");
      if (err instanceof ShfdError && err.status === 401) {
        sessions.dropToken(s.token);
        return reply.code(401).send({ error: "not signed in" });
      }
      if (err instanceof ShfdError && err.status === 403) {
        sessions.delete(s.id);
        return reply.code(403).send({ error: loginNotAdmin(langOf(req)) });
      }
      if (err instanceof ShfdError && err.code !== "unreachable" && err.code !== "internal") {
        return reply.code(err.status).send({ error: errorMessage(err.code, err.message, langOf(req)), code: err.code });
      }
      req.log.warn({ err: (err as Error).message }, "file content failed");
      return reply.code(502).send({ error: errorMessage("unreachable", undefined, langOf(req)) });
    }
    reply.code(r.status);
    for (const h of CONTENT_RESPONSE_HEADERS) {
      const v = r.headers.get(h);
      if (v) reply.header(h, v);
    }
    if (!r.body || r.status === 304 || req.method === "HEAD") {
      await r.body?.cancel();
      return reply.send();
    }
    return reply.send(Readable.fromWeb(r.body as import("node:stream/web").ReadableStream<Uint8Array>));
  });

  // ---- the web app -------------------------------------------------------

  if (base !== "") app.get(base, async (_req, reply) => reply.redirect(`${base}/`));
  if (o.webDir) {
    const root = normalize(o.webDir);
    const serve = async (req: FastifyRequest, reply: FastifyReply) => {
      const rel = decodeURIComponent((req.params as { "*": string })["*"] ?? "");
      let file = normalize(join(root, rel));
      if (file !== root && !file.startsWith(root + sep)) return reply.code(404).send();
      const st = await stat(file).catch(() => undefined);
      if (!st || st.isDirectory()) file = join(root, "index.html");
      const ext = extname(file);
      const body = ext === ".html" ? withBase(await readFile(file, "utf8"), base) : await readFile(file);
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

/**
 * index.html with the base written in: a <base href> for its relative
 * assets (the build is the same for every base) and the meta the web app
 * reads its base from (web/src/api.ts).
 */
export function withBase(html: string, base: string): string {
  const b = escapeAttr(base);
  const tags = `<base href="${b}/" />\n    <meta name="${BASE_META}" content="${b}" />`;
  return html.replace(/<head>/i, (h) => `${h}\n    ${tags}`);
}

function escapeAttr(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

function publicSession(s: { shareId: string; shareName: string; username: string; role: string }) {
  return { share_id: s.shareId, share_name: s.shareName, username: s.username, role: s.role };
}
