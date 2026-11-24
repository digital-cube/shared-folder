// CSRF protection of shfd-gui. The session cookie is SameSite=Strict, so a
// browser does not send it on a request another site starts; on top of
// that every request that changes something (POST, PUT, PATCH, DELETE to
// <base>/api/… and <base>/udc/…) must come from a page of this host: its
// Origin, or without one its Referer, names the host the request was sent
// to. A request with neither is refused too (every browser sends Origin
// with a POST made by fetch).

import type { FastifyReply, FastifyRequest } from "fastify";
import { tr, type Lang } from "./lang.js";

export const UNSAFE_METHODS = new Set(["POST", "PUT", "PATCH", "DELETE"]);

/**
 * Whether the request comes from a page of host. host is the name the
 * request was sent to (Fastify's req.hostname: Host, or X-Forwarded-Host
 * from a trusted proxy), without a port: nginx passes Host as $host, which
 * has none, so only the names are compared.
 */
export function sameOrigin(headers: { origin?: string | string[]; referer?: string | string[] }, host: string): boolean {
  const one = (v: string | string[] | undefined) => (Array.isArray(v) ? v[0] : v);
  const origin = one(headers.origin);
  const source = origin !== undefined && origin !== "" ? origin : one(headers.referer);
  if (!source || source === "null") return false;
  let name: string;
  try {
    name = new URL(source).hostname;
  } catch {
    return false;
  }
  return name !== "" && name.toLowerCase() === hostOnly(host).toLowerCase();
}

/** "example.org:8443" → "example.org", "[::1]:80" → "[::1]". */
export function hostOnly(host: string): string {
  if (host.startsWith("[")) {
    const i = host.indexOf("]");
    return i > 0 ? host.slice(0, i + 1) : host;
  }
  const i = host.lastIndexOf(":");
  return i > 0 && host.indexOf(":") === i ? host.slice(0, i) : host;
}

export const CSRF_MESSAGE = "Zahtev nije poslat sa ove stranice; osveži stranicu i pokušaj ponovo.";
export const CSRF_MESSAGE_EN = "The request was not sent from this page; reload the page and try again.";

/** The onRequest hook: refuses an unsafe request to the GUI's own paths from another origin. */
export function csrfHook(base: string, langOf: (req: FastifyRequest) => Lang) {
  const guarded = [`${base}/api/`, `${base}/udc/`];
  return async (req: FastifyRequest, reply: FastifyReply) => {
    if (!UNSAFE_METHODS.has(req.method)) return;
    if (!guarded.some((p) => req.url.startsWith(p))) return;
    if (sameOrigin(req.headers, req.hostname)) return;
    const lang = langOf(req);
    return reply.code(403).send({ error: tr(lang, CSRF_MESSAGE, CSRF_MESSAGE_EN), code: "csrf" });
  };
}
