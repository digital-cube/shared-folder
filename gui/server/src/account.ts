// Sign-in, sign-out, the folder choice and "Moj nalog" of shfd-gui: the
// GUI's own routes under <base>/api/. A password sign-in makes a GUI
// device of the account on shfd (POST /v1/auth/login kind gui) and keeps
// its token in the session; everything after it, folder routes included,
// is called with that token. The member-token sign-in of before stays as a
// hidden option ("Prijava tokenom") while SHFD_GUI_TOKEN_LOGIN is not 0.

import type { FastifyInstance, FastifyReply, FastifyRequest } from "fastify";
import { langOf, tr, type Lang } from "./lang.js";
import { COOKIE, readCookie, sessionCookie, type Session, type SessionStore } from "./sessions.js";
import { errorMessage, ShfdError, type ShfdClient } from "./shfd.js";

export const LOGIN_INVALID = "Token nije ispravan ili je opozvan.";
export const LOGIN_NOT_ADMIN = "Samo admin foldera može da se prijavi tokenom.";
export const LOGIN_INVALID_EN = "The token is not valid or has been revoked.";
export const LOGIN_NOT_ADMIN_EN = "Only an admin of the folder can sign in with a token.";
/** A wrong password, an unknown and a disabled account: one sentence for all, as shfd answers. */
export const LOGIN_BAD_PASSWORD = "Pogrešno korisničko ime ili lozinka.";
export const LOGIN_BAD_PASSWORD_EN = "Wrong username or password.";

const loginInvalid = (lang: Lang) => tr(lang, LOGIN_INVALID, LOGIN_INVALID_EN);
const loginNotAdmin = (lang: Lang) => tr(lang, LOGIN_NOT_ADMIN, LOGIN_NOT_ADMIN_EN);
const badPassword = (lang: Lang) => tr(lang, LOGIN_BAD_PASSWORD, LOGIN_BAD_PASSWORD_EN);

/** "Previše neuspelih pokušaja…": the pause after several failures. */
export function tooMany(lang: Lang, seconds?: number): string {
  if (!seconds) return tr(lang, "Previše neuspelih pokušaja. Pokušaj ponovo malo kasnije.", "Too many failed attempts. Try again a little later.");
  const s = seconds < 60 ? tr(lang, `${seconds} s`, `${seconds} s`) : tr(lang, `${Math.ceil(seconds / 60)} min`, `${Math.ceil(seconds / 60)} min`);
  return tr(lang, `Previše neuspelih pokušaja. Pokušaj ponovo za ${s}.`, `Too many failed attempts. Try again in ${s}.`);
}

/**
 * The name of the GUI device on shfd: "gui · Firefox (Linux)". What the
 * account's "Moji uređaji" list shows for this browser.
 */
export function browserName(ua: string | undefined): string {
  const u = ua ?? "";
  const browser = /Edg\//.test(u)
    ? "Edge"
    : /OPR\//.test(u)
      ? "Opera"
      : /Firefox\//.test(u)
        ? "Firefox"
        : /Chrome\//.test(u)
          ? "Chrome"
          : /Safari\//.test(u)
            ? "Safari"
            : "browser";
  const os = /Android/.test(u)
    ? "Android"
    : /iPhone|iPad/.test(u)
      ? "iOS"
      : /Mac OS X|Macintosh/.test(u)
        ? "macOS"
        : /Windows/.test(u)
          ? "Windows"
          : /Linux/.test(u)
            ? "Linux"
            : "";
  return `gui · ${browser}${os ? ` (${os})` : ""}`;
}

/** What the browser learns of its session: never the token. */
export function publicSession(s: Session) {
  return {
    kind: s.kind,
    username: s.username,
    display_name: s.displayName,
    must_change: s.mustChange,
    share_id: s.shareId,
    share_name: s.shareName,
    role: s.role,
    member: s.member,
  };
}

export interface AccountRoutesOptions {
  base: string;
  shfd: ShfdClient;
  sessions: SessionStore;
  cookieOpts: { path: string; secure: boolean };
  tokenLogin: boolean;
  /** Called when the folder of a session changed (the browser reconnects its change feed). */
  onFolder?: (s: Session) => void;
}

export function registerAccountRoutes(app: FastifyInstance, o: AccountRoutesOptions): void {
  const { base, shfd, sessions, cookieOpts } = o;
  const sessionOf = (req: FastifyRequest) => sessions.get(readCookie(req.headers.cookie, COOKIE));
  const clear = (reply: FastifyReply) => reply.header("set-cookie", sessionCookie("", { ...cookieOpts, maxAgeSec: 0 }));
  const notSignedIn = (reply: FastifyReply, lang: Lang) =>
    reply.code(401).send({ error: tr(lang, "Prijava je istekla.", "The sign-in has expired."), code: "unauthenticated" });

  // A refusal of shfd as an answer to the browser. 401: the device token is
  // dead (revoked, expired, the account disabled), so is the session.
  const refuse = (req: FastifyRequest, reply: FastifyReply, s: Session | undefined, err: unknown) => {
    const lang = langOf(req);
    if (err instanceof ShfdError) {
      if (err.status === 401) {
        if (s) sessions.dropToken(s.token);
        clear(reply);
        return notSignedIn(reply, lang);
      }
      if (err.code === "too_many_attempts") {
        if (err.retryAfter) reply.header("retry-after", String(err.retryAfter));
        return reply.code(429).send({ error: tooMany(lang, err.retryAfter), code: err.code, retry_after: err.retryAfter ?? null });
      }
      if (err.code !== "unreachable" && err.code !== "internal") {
        return reply.code(err.status).send({ error: errorMessage(err.code, err.message, lang), code: err.code });
      }
    }
    req.log.warn({ err: (err as Error).message }, "shfd call failed");
    return reply.code(502).send({ error: errorMessage("unreachable", undefined, lang), code: "unreachable" });
  };

  const accountOf = (req: FastifyRequest, reply: FastifyReply): Session | undefined => {
    const s = sessionOf(req);
    if (!s) {
      void notSignedIn(reply, langOf(req));
      return undefined;
    }
    if (s.kind !== "account") {
      void reply.code(400).send({ error: tr(langOf(req), "Prijava tokenom nema nalog.", "A token sign-in has no account."), code: "no_account" });
      return undefined;
    }
    return s;
  };

  app.get(`${base}/api/config`, async (_req, reply) => {
    reply.header("cache-control", "no-store");
    return { token_login: o.tokenLogin };
  });

  // ---- password sign-in ----------------------------------------------------

  app.post(`${base}/api/auth/login`, async (req, reply) => {
    const lang = langOf(req);
    const b = (req.body ?? {}) as { username?: unknown; password?: unknown };
    const username = typeof b.username === "string" ? b.username.trim() : "";
    const password = typeof b.password === "string" ? b.password : "";
    if (!username || !password) return reply.code(400).send({ error: badPassword(lang), code: "invalid_credentials" });
    let r;
    try {
      r = await shfd.login(username, password, browserName(req.headers["user-agent"]));
    } catch (err) {
      if (err instanceof ShfdError && (err.code === "invalid_credentials" || err.status === 401)) {
        return reply.code(401).send({ error: badPassword(lang), code: "invalid_credentials" });
      }
      return refuse(req, reply, undefined, err);
    }
    const s = sessions.create({
      kind: "account",
      token: r.token,
      username: r.user.username,
      displayName: r.user.display_name,
      mustChange: r.must_change,
    });
    reply.header("set-cookie", sessionCookie(s.id, cookieOpts));
    reply.header("cache-control", "no-store");
    return publicSession(s);
  });

  // ---- member-token sign-in (hidden; SHFD_GUI_TOKEN_LOGIN=0 turns it off) ---

  app.post(`${base}/api/login`, async (req, reply) => {
    const lang = langOf(req);
    if (!o.tokenLogin) {
      return reply.code(404).send({ error: tr(lang, "Prijava tokenom je isključena.", "Sign-in with a token is turned off."), code: "token_login_off" });
    }
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
      kind: "token",
      token,
      shareId: who.share_id,
      shareName: who.share_name,
      username: who.username,
      member: who.username,
      role: who.role,
    });
    reply.header("set-cookie", sessionCookie(s.id, cookieOpts));
    return publicSession(s);
  });

  // ---- sign-out ------------------------------------------------------------

  app.post(`${base}/api/logout`, async (req, reply) => {
    const s = sessionOf(req);
    if (s) {
      sessions.delete(s.id);
      if (s.kind === "account") {
        // The device goes on shfd too; a token that is already dead is fine.
        await shfd.logout(s.token).catch((err) => {
          if (!(err instanceof ShfdError && err.status === 401)) req.log.warn({ err: (err as Error).message }, "logout on shfd failed");
        });
      }
    }
    clear(reply);
    return { ok: true };
  });

  app.get(`${base}/api/session`, async (req, reply) => {
    reply.header("cache-control", "no-store");
    const s = sessionOf(req);
    if (!s) return reply.code(401).send({ error: "not signed in" });
    return publicSession(s);
  });

  // ---- the account: folders, devices, password -----------------------------

  app.get(`${base}/api/me`, async (req, reply) => {
    reply.header("cache-control", "no-store");
    const s = accountOf(req, reply);
    if (!s) return reply;
    try {
      const me = await shfd.me(s.token);
      s.mustChange = me.user.must_change;
      s.displayName = me.user.display_name;
      // The chosen folder as it is now: renamed, another role, or gone.
      if (s.shareId) {
        const f = me.folders.find((x) => x.share_id === s.shareId);
        if (f) Object.assign(s, { shareName: f.name, role: f.role, member: f.member });
        else Object.assign(s, { shareId: "", shareName: "", role: "", member: s.username });
      }
      return { ...me, session: publicSession(s) };
    } catch (err) {
      return refuse(req, reply, s, err);
    }
  });

  app.post(`${base}/api/folder`, async (req, reply) => {
    const lang = langOf(req);
    const s = accountOf(req, reply);
    if (!s) return reply;
    if (s.mustChange) return reply.code(403).send({ error: errorMessage("password_change_required", undefined, lang), code: "password_change_required" });
    const id = String((req.body as { share_id?: unknown } | undefined)?.share_id ?? "");
    try {
      const me = await shfd.me(s.token);
      const f = me.folders.find((x) => x.share_id === id);
      if (!f) return reply.code(404).send({ error: tr(lang, "Nisi član tog foldera.", "You are not a member of that folder."), code: "unknown_share" });
      Object.assign(s, { shareId: f.share_id, shareName: f.name, role: f.role, member: f.member });
      o.onFolder?.(s);
      return publicSession(s);
    } catch (err) {
      return refuse(req, reply, s, err);
    }
  });

  app.post(`${base}/api/me/password`, async (req, reply) => {
    const lang = langOf(req);
    const s = accountOf(req, reply);
    if (!s) return reply;
    const b = (req.body ?? {}) as { old?: unknown; new?: unknown };
    const oldPw = typeof b.old === "string" ? b.old : "";
    const newPw = typeof b.new === "string" ? b.new : "";
    if (!oldPw || !newPw) return reply.code(400).send({ error: tr(lang, "Upiši staru i novu lozinku.", "Enter the old and the new password."), code: "invalid_request" });
    try {
      await shfd.changePassword(s.token, oldPw, newPw);
    } catch (err) {
      if (err instanceof ShfdError && err.code === "invalid_password") {
        return reply.code(400).send({ error: passwordRuleMessage(err.message, lang), code: err.code });
      }
      return refuse(req, reply, s, err);
    }
    s.mustChange = false;
    return publicSession(s);
  });

  app.delete(`${base}/api/me/devices/:id`, async (req, reply) => {
    const lang = langOf(req);
    const s = accountOf(req, reply);
    if (!s) return reply;
    const id = Number((req.params as { id: string }).id);
    if (!Number.isInteger(id) || id <= 0) return reply.code(400).send({ error: errorMessage("unknown_device", undefined, lang), code: "unknown_device" });
    try {
      const me = await shfd.me(s.token);
      const current = me.devices.find((d) => d.current)?.id === id;
      await shfd.revokeMyDevice(s.token, id);
      if (current) {
        // This browser's own session: revoking it signs it out.
        sessions.dropToken(s.token);
        clear(reply);
        return { ok: true, signed_out: true };
      }
      return { ok: true, signed_out: false };
    } catch (err) {
      return refuse(req, reply, s, err);
    }
  });
}

/** shfd's invalid_password message (English, from password.Validate) as a sentence of the UI. */
export function passwordRuleMessage(msg: string, lang: Lang): string {
  let m = /at least (\d+) characters/.exec(msg);
  if (m) return tr(lang, `Lozinka mora imati bar ${m[1]} znakova.`, `The password must have at least ${m[1]} characters.`);
  m = /at most (\d+) characters/.exec(msg);
  if (m) return tr(lang, `Lozinka može imati najviše ${m[1]} znakova.`, `The password may have at most ${m[1]} characters.`);
  if (/username/.test(msg)) return tr(lang, "Lozinka ne sme biti isto što i korisničko ime.", "The password may not be the username.");
  if (/differ/.test(msg)) return tr(lang, "Nova lozinka mora da se razlikuje od stare.", "The new password must differ from the old one.");
  return tr(lang, `Lozinka nije prihvaćena: ${msg}`, `The password was not accepted: ${msg}`);
}
