// Password sign-in, must_change, expiry, CSRF, the folder choice, the
// rights of a member who is not an admin, members as accounts, devices and
// the token sign-in switch (TCKJHR5PHM).

import { afterEach, describe, expect, it } from "vitest";
import { browserName, passwordRuleMessage, tooMany } from "../src/account.js";
import { buildGui, LOGIN_BAD_PASSWORD, type GuiApp } from "../src/app.js";
import { hostOnly, sameOrigin } from "../src/csrf.js";
import { IDLE_MS, MAX_AGE_MS } from "../src/sessions.js";
import { FakeShfd } from "./fake-shfd.js";
import { bare, fromPage } from "./origin.js";

let gui: GuiApp | undefined;
afterEach(async () => {
  await gui?.app.close();
  gui = undefined;
});

const FIREFOX = "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0";
const OTHER = "6b1d0000-0000-4000-8000-000000000002";

/** ana: admin of "projekti" and a member of "arhiva"; bob: a member of "projekti" only. */
async function setup(o: { tokenLogin?: boolean; base?: string } = {}) {
  const fake = new FakeShfd();
  fake.members = [];
  fake.user("ana", "anina-lozinka-1");
  fake.user("bob", "bobova-lozinka-1");
  fake.addAccountMember("ana", "admin");
  fake.addAccountMember("bob", "member");
  fake.folders.push({ share_id: OTHER, name: "arhiva", members: { ana: "member" } });
  let t = Date.parse("2026-11-24T12:00:00Z");
  const clock = { now: () => t, advance: (ms: number) => (t += ms) };
  gui = await buildGui({
    shfdUrl: "http://shfd",
    base: o.base ?? "/gui",
    secureCookie: true,
    fetchFn: fake.fetch,
    pollMs: 60_000,
    tokenLogin: o.tokenLogin,
    now: clock.now,
  });
  return { fake, app: fromPage(gui.app), clock, base: o.base ?? "/gui" };
}

type App = GuiApp["app"];

async function signIn(app: App, username: string, password: string, base = "/gui") {
  const res = await app.inject({ method: "POST", url: `${base}/api/auth/login`, payload: { username, password }, headers: { "user-agent": FIREFOX } });
  const cookie = String(res.headers["set-cookie"] ?? "").split(";")[0];
  return { res, cookie };
}

async function udc(app: App, cookie: string, action: string, payload: unknown) {
  const res = await app.inject({ method: "POST", url: `/gui/udc/${action}`, payload: payload as object, headers: { cookie } });
  return res.json() as { kind: string; payload: any };
}

/** A host action: kind is udc.<name>.ok or .err. */
const call = async (app: App, cookie: string, name: string, payload: unknown) => {
  const r = await udc(app, cookie, name, payload);
  return { ...r, ok: r.kind === `udc.${name}.ok` };
};

async function choose(app: App, cookie: string, share: string) {
  return app.inject({ method: "POST", url: "/gui/api/folder", payload: { share_id: share }, headers: { cookie } });
}

describe("password sign-in", () => {
  it("makes a GUI device on shfd and keeps its token only on the server", async () => {
    const { fake, app } = await setup();
    const { res, cookie } = await signIn(app, "ana", "anina-lozinka-1");
    expect(res.statusCode).toBe(200);
    expect(res.json()).toEqual({
      kind: "account",
      username: "ana",
      display_name: "",
      must_change: false,
      share_id: "",
      share_name: "",
      role: "",
      member: "ana",
      avatar_url: null,
    });
    const set = String(res.headers["set-cookie"]);
    expect(set).toMatch(/HttpOnly/);
    expect(set).toMatch(/Secure/);
    expect(set).toMatch(/SameSite=Strict/);
    expect(set).toMatch(/Path=\/gui\//);
    const token = fake.devices.at(-1)!.token;
    expect(set).not.toContain(token);
    expect(res.body).not.toContain(token);
    expect(fake.lastDeviceName).toBe("gui · Firefox (Linux)");
    expect(fake.devices.at(-1)!.kind).toBe("gui");
    const s = await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } });
    expect(s.json().username).toBe("ana");
  });

  it("a wrong password, an unknown and a disabled account get one sentence", async () => {
    const { fake, app } = await setup();
    fake.users[1].disabled = true;
    for (const [u, p] of [
      ["ana", "pogresna-lozinka"],
      ["niko", "anina-lozinka-1"],
      ["bob", "bobova-lozinka-1"],
    ]) {
      const { res } = await signIn(app, u, p);
      expect(res.statusCode).toBe(401);
      expect(res.json()).toEqual({ error: LOGIN_BAD_PASSWORD, code: "invalid_credentials" });
      expect(res.headers["set-cookie"]).toBeUndefined();
    }
    expect(gui!.sessions.size()).toBe(0);
  });

  it("after several failures says how long to wait", async () => {
    const { fake, app } = await setup();
    fake.failLimit = 2;
    await signIn(app, "ana", "x1");
    await signIn(app, "ana", "x2");
    const { res } = await signIn(app, "ana", "anina-lozinka-1");
    expect(res.statusCode).toBe(429);
    expect(res.headers["retry-after"]).toBe("4");
    expect(res.json().error).toBe("Previše neuspelih pokušaja. Pokušaj ponovo za 4 s.");
    expect(tooMany("en", 600)).toBe("Too many failed attempts. Try again in 10 min.");
  });

  it("must_change: only the password change is open until it is done", async () => {
    const { fake, app } = await setup();
    fake.users[0].must_change = true;
    const { res, cookie } = await signIn(app, "ana", "anina-lozinka-1");
    expect(res.json().must_change).toBe(true);
    // Nothing of a folder: no choice, no collections, no files.
    expect((await choose(app, cookie, fake.share)).statusCode).toBe(403);
    const l = await udc(app, cookie, "list", { collection: "files" });
    expect(l.kind).toBe("udc.list.err");
    expect((await app.inject({ method: "GET", url: "/gui/api/files/content?path=a.txt", headers: { cookie } })).statusCode).toBe(409);
    const pw = (payload: object) => app.inject({ method: "POST", url: "/gui/api/me/password", payload, headers: { cookie } });
    const wrong = await pw({ old: "nije-ta", new: "nova-dugacka-lozinka" });
    expect(wrong.statusCode).toBe(403);
    expect(wrong.json().error).toBe("Stara lozinka nije tačna.");
    const short = await pw({ old: "anina-lozinka-1", new: "kratka" });
    expect(short.statusCode).toBe(400);
    expect(short.json().error).toBe("Lozinka mora imati bar 12 znakova.");
    const ok = await pw({ old: "anina-lozinka-1", new: "nova-dugacka-lozinka" });
    expect(ok.statusCode).toBe(200);
    expect(ok.json().must_change).toBe(false);
    expect((await choose(app, cookie, fake.share)).statusCode).toBe(200);
  });

  it("expires like shfd's GUI device: 12 h without use, 7 days at most", async () => {
    const { app, clock } = await setup();
    const a = await signIn(app, "ana", "anina-lozinka-1");
    clock.advance(IDLE_MS + 1000);
    expect((await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie: a.cookie } })).statusCode).toBe(401);

    const b = await signIn(app, "ana", "anina-lozinka-1");
    // Used every 11 hours, it still ends 7 days after the sign-in.
    for (let used = 0; used + 11 * 3600_000 < MAX_AGE_MS; used += 11 * 3600_000) {
      clock.advance(11 * 3600_000);
      expect((await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie: b.cookie } })).statusCode).toBe(200);
    }
    clock.advance(11 * 3600_000);
    expect((await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie: b.cookie } })).statusCode).toBe(401);
  });

  it("a device token shfd no longer takes ends the session at the next call", async () => {
    const { fake, app } = await setup();
    const { cookie } = await signIn(app, "ana", "anina-lozinka-1");
    await choose(app, cookie, fake.share);
    fake.devices.at(-1)!.revoked_at = "2026-11-24T12:30:00Z";
    const l = await udc(app, cookie, "list", { collection: "members" });
    expect(l.payload.details.reason).toBe("unauthenticated");
    expect((await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } })).statusCode).toBe(401);
  });

  it("sign-out revokes the GUI device on shfd", async () => {
    const { fake, app } = await setup();
    const { cookie } = await signIn(app, "ana", "anina-lozinka-1");
    const out = await app.inject({ method: "POST", url: "/gui/api/logout", headers: { cookie } });
    expect(out.statusCode).toBe(200);
    expect(String(out.headers["set-cookie"])).toMatch(/Max-Age=0/);
    expect(fake.calls).toContain("POST /v1/auth/logout");
    expect(fake.devices.at(-1)!.revoked_at).toBeTruthy();
    expect((await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } })).statusCode).toBe(401);
  });

  it("works under the root base too", async () => {
    const { app } = await setup({ base: "" });
    const { res } = await signIn(app, "ana", "anina-lozinka-1", "");
    expect(res.statusCode).toBe(200);
    expect(String(res.headers["set-cookie"])).toMatch(/Path=\/;/);
  });

  it("names the browser of the device", () => {
    expect(browserName(FIREFOX)).toBe("gui · Firefox (Linux)");
    expect(browserName("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15")).toBe(
      "gui · Safari (macOS)",
    );
    expect(browserName("Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/140.0 Safari/537.36 Edg/140.0")).toBe("gui · Edge (Windows)");
    expect(browserName(undefined)).toBe("gui · browser");
    expect(passwordRuleMessage("the password may not be the username", "en")).toBe("The password may not be the username.");
  });
});

describe("CSRF", () => {
  it("refuses a POST, PUT or DELETE that does not come from a page of this host", async () => {
    const { fake, app } = await setup();
    const { cookie } = await signIn(app, "ana", "anina-lozinka-1");
    const bad = [
      { method: "POST" as const, url: "/gui/api/folder", headers: { cookie, origin: "https://evil.example" }, payload: { share_id: fake.share } },
      { method: "POST" as const, url: "/gui/api/logout", headers: { cookie, referer: "https://evil.example/x" } },
      { method: "POST" as const, url: "/gui/api/auth/login", headers: { origin: "null" }, payload: { username: "ana", password: "anina-lozinka-1" } },
      { method: "DELETE" as const, url: "/gui/api/me/devices/101", headers: { cookie, origin: "http://localhost.evil.example" } },
      { method: "POST" as const, url: "/gui/udc/list", headers: { cookie, origin: "https://evil.example" }, payload: { collection: "members" } },
    ];
    for (const r of bad) {
      const res = await app.inject(r);
      expect(res.statusCode, `${r.method} ${r.url}`).toBe(403);
      expect(res.json().code).toBe("csrf");
    }
    // Neither Origin nor Referer: refused as well (inject without fromPage's default).
    const none = await bare(app)({ method: "POST", url: "/gui/api/logout", headers: { cookie } });
    expect(none.statusCode).toBe(403);
    // Still signed in: none of them did anything.
    expect((await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } })).statusCode).toBe(200);
    expect(fake.calls).not.toContain("POST /v1/auth/logout");
    // The page's own Origin, or its Referer, passes; a GET needs neither.
    expect((await app.inject({ method: "POST", url: "/gui/api/folder", payload: { share_id: fake.share }, headers: { cookie, origin: "https://localhost" } })).statusCode).toBe(200);
    expect((await app.inject({ method: "POST", url: "/gui/api/folder", payload: { share_id: fake.share }, headers: { cookie, referer: "http://localhost/gui/#files" } })).statusCode).toBe(200);
    expect((await bare(app)({ method: "GET", url: "/gui/api/session", headers: { cookie } })).statusCode).toBe(200);
  });

  it("compares the names the browser and the proxy give", () => {
    expect(sameOrigin({ origin: "https://files.example.org" }, "files.example.org")).toBe(true);
    expect(sameOrigin({ origin: "https://files.example.org:8444" }, "files.example.org")).toBe(true);
    expect(sameOrigin({ origin: "https://FILES.example.org" }, "files.example.org")).toBe(true);
    expect(sameOrigin({ origin: "https://files.example.org.evil.io" }, "files.example.org")).toBe(false);
    expect(sameOrigin({ referer: "https://files.example.org/gui/" }, "files.example.org")).toBe(true);
    expect(sameOrigin({ origin: "", referer: "https://evil.io/" }, "files.example.org")).toBe(false);
    expect(sameOrigin({ origin: "not a url" }, "files.example.org")).toBe(false);
    expect(sameOrigin({}, "files.example.org")).toBe(false);
    expect(hostOnly("files.example.org:443")).toBe("files.example.org");
    expect(hostOnly("[::1]:8444")).toBe("[::1]");
  });
});

describe("the folder choice", () => {
  it("lists my folders with role and last change, and the choice holds every folder route", async () => {
    const { fake, app } = await setup();
    const { cookie } = await signIn(app, "ana", "anina-lozinka-1");
    const me = (await app.inject({ method: "GET", url: "/gui/api/me", headers: { cookie } })).json();
    expect(me.folders).toEqual([
      { share_id: fake.share, name: "projekti", role: "admin", member: "ana", last_change_at: "2026-11-24T10:00:00Z" },
      { share_id: OTHER, name: "arhiva", role: "member", member: "ana", last_change_at: "2026-11-20T08:00:00Z" },
    ]);
    expect(me.password_rules.min_len).toBe(12);
    expect(JSON.stringify(me)).not.toContain(fake.devices.at(-1)!.token);
    // No folder yet: the collections say so.
    const none = await udc(app, cookie, "list", { collection: "files" });
    expect(none.kind).toBe("udc.list.err");
    expect(none.payload.message).toBe("Izaberi folder.");

    const c = await choose(app, cookie, fake.share);
    expect(c.json()).toMatchObject({ share_id: fake.share, share_name: "projekti", role: "admin" });
    expect((await udc(app, cookie, "list", { collection: "members" })).kind).toBe("udc.list.ok");

    const c2 = await choose(app, cookie, OTHER);
    expect(c2.json()).toMatchObject({ share_id: OTHER, share_name: "arhiva", role: "member" });
    fake.calls.length = 0;
    const files = await udc(app, cookie, "list", { collection: "files" });
    expect(files.kind).toBe("udc.list.ok");
    expect(fake.calls).toContain(`GET /v1/shares/${OTHER}/files?dir=`);
    // The device token went with it, not a folder token.
    const token = fake.devices.at(-1)!.token;
    expect(token.startsWith("shd_")).toBe(true);

    const nope = await choose(app, cookie, "00000000-0000-4000-8000-000000000000");
    expect(nope.statusCode).toBe(404);
    expect(nope.json().error).toBe("Nisi član tog foldera.");
  });

  it("a folder the account left is dropped from the session at the next /api/me", async () => {
    const { fake, app } = await setup();
    const { cookie } = await signIn(app, "ana", "anina-lozinka-1");
    await choose(app, cookie, OTHER);
    fake.folders[0].members = {};
    const me = (await app.inject({ method: "GET", url: "/gui/api/me", headers: { cookie } })).json();
    expect(me.session.share_id).toBe("");
  });
});

describe("a member who is not an admin", () => {
  it("browses and downloads; members, logs, trash and changes are refused (shfd's 403)", async () => {
    const { fake, app } = await setup();
    fake.contents.set("a.txt", new TextEncoder().encode("abc"));
    fake.thumbs.set("aa".repeat(32), new Uint8Array([0xff, 0xd8, 0xff]));
    const { cookie } = await signIn(app, "bob", "bobova-lozinka-1");
    expect((await choose(app, cookie, fake.share)).json().role).toBe("member");

    const files = await udc(app, cookie, "list", { collection: "files" });
    expect(files.kind).toBe("udc.list.ok");
    expect(files.payload.rows.map((r: any) => r.name)).toEqual(["docs", "a.txt"]);
    const dl = await app.inject({ method: "GET", url: "/gui/api/files/content?path=a.txt&download=1", headers: { cookie } });
    expect(dl.statusCode).toBe(200);
    expect(dl.body).toBe("abc");
    const th = await app.inject({ method: "GET", url: `/gui/api/thumb/${"aa".repeat(32)}?size=m`, headers: { cookie } });
    expect(th.statusCode).toBe(200);
    expect(fake.calls).not.toContain("ADMIN-FILE-ROUTE");

    for (const collection of ["members", "logs", "trash"]) {
      const l = await udc(app, cookie, "list", { collection });
      expect(l.kind, collection).toBe("udc.list.err");
      expect(l.payload.message).toBe("To može samo admin foldera.");
    }
    const rn = await call(app, cookie, "shfd.file_rename", { path: "a.txt", name: "b.txt", base_hash: "aa".repeat(32) });
    expect(rn.ok).toBe(false);
    expect(rn.payload.message).toBe("To može samo admin foldera.");
    expect(fake.ops).toEqual([]);
    const add = await call(app, cookie, "shfd.member_add_account", { user: "bob", admin: true });
    expect(add.ok).toBe(false);
    const ov = await call(app, cookie, "shfd.overview", {});
    expect(ov.payload).toMatchObject({ limited: true, server_ok: true, version: "abc1234" });
    // Still signed in: a refusal does not sign a member out.
    expect((await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } })).statusCode).toBe(200);
  });

  it("an admin who loses the role stays signed in as a member", async () => {
    const { fake, app } = await setup();
    const { cookie } = await signIn(app, "ana", "anina-lozinka-1");
    await choose(app, cookie, fake.share);
    fake.members[0].role = "member";
    const l = await udc(app, cookie, "list", { collection: "members" });
    expect(l.kind).toBe("udc.list.err");
    const s = (await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } })).json();
    expect(s.role).toBe("member");
  });
});

describe("members as accounts", () => {
  it("adds an account found by search; a member without one is marked legacy", async () => {
    const { fake, app } = await setup();
    fake.user("cica", "cicina-lozinka-1", { displayName: "Cica C." });
    fake.add("stari-laptop", "member");
    const { cookie } = await signIn(app, "ana", "anina-lozinka-1");
    await choose(app, cookie, fake.share);
    const found = await call(app, cookie, "shfd.users_search", { q: "ci" });
    expect(found.payload).toEqual({ users: [{ username: "cica", display_name: "Cica C." }] });
    const add = await call(app, cookie, "shfd.member_add_account", { user: "cica", admin: false });
    expect(add.payload).toEqual({ username: "cica", account: "cica", reactivated: false });
    expect(fake.calls).toContain(`POST /v1/shares/${fake.share}/admin/members`);
    const again = await call(app, cookie, "shfd.member_add_account", { user: "cica", admin: false });
    expect(again.payload.message).toBe("Član sa tim imenom već postoji.");

    const rows = (await udc(app, cookie, "list", { collection: "members" })).payload.rows;
    const byName = Object.fromEntries(rows.map((r: any) => [r.username, r]));
    expect(byName.cica).toMatchObject({ account: "cica", legacy: false, devices: 0 });
    expect(byName["stari-laptop"]).toMatchObject({ account: "", legacy: true });
    expect(byName.ana.me).toBe(true);
  });

  it("shows a member's devices and revokes one; the others keep working", async () => {
    const { fake, app } = await setup();
    const mac = fake.clientDevice("bob", "macbook");
    fake.clientDevice("bob", "linux-mare");
    const { cookie } = await signIn(app, "ana", "anina-lozinka-1");
    await choose(app, cookie, fake.share);
    const list = await call(app, cookie, "shfd.member_devices", { username: "bob" });
    expect(list.payload.account).toBe("bob");
    expect(list.payload.devices.map((d: any) => [d.name, d.kind, d.revoked])).toEqual([
      ["macbook", "client", false],
      ["linux-mare", "client", false],
    ]);
    expect(list.payload.devices[0].ssh_fingerprint).toMatch(/^SHA256:/);
    const r = await call(app, cookie, "shfd.member_device_revoke", { username: "bob", device: mac.id });
    expect(r.ok).toBe(true);
    expect(fake.calls).toContain(`DELETE /v1/shares/${fake.share}/admin/members/bob/devices/${mac.id}`);
    expect(r.payload.devices.map((d: any) => [d.name, d.revoked])).toEqual([
      ["macbook", true],
      ["linux-mare", false],
    ]);
    const rows = (await udc(app, cookie, "list", { collection: "members" })).payload.rows;
    expect(rows.find((x: any) => x.username === "bob").devices).toBe(1);
  });
});

describe("Moj nalog: my devices", () => {
  it("revokes another device and stays signed in; revoking this session signs out", async () => {
    const { fake, app } = await setup();
    const phone = fake.clientDevice("ana", "telefon");
    const { cookie } = await signIn(app, "ana", "anina-lozinka-1");
    const me = (await app.inject({ method: "GET", url: "/gui/api/me", headers: { cookie } })).json();
    const current = me.devices.find((d: any) => d.current);
    expect(current.kind).toBe("gui");
    expect(me.devices.map((d: any) => d.name)).toEqual(["telefon", "gui · Firefox (Linux)"]);

    const other = await app.inject({ method: "DELETE", url: `/gui/api/me/devices/${phone.id}`, headers: { cookie } });
    expect(other.json()).toEqual({ ok: true, signed_out: false });
    expect(phone.revoked_at).toBeTruthy();
    expect((await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } })).statusCode).toBe(200);

    const self = await app.inject({ method: "DELETE", url: `/gui/api/me/devices/${current.id}`, headers: { cookie } });
    expect(self.json()).toEqual({ ok: true, signed_out: true });
    expect(String(self.headers["set-cookie"])).toMatch(/Max-Age=0/);
    expect((await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } })).statusCode).toBe(401);

    const unknown = await signIn(app, "ana", "anina-lozinka-1");
    const u = await app.inject({ method: "DELETE", url: "/gui/api/me/devices/999", headers: { cookie: unknown.cookie } });
    expect(u.statusCode).toBe(404);
    expect(u.json().error).toBe("Taj uređaj ne postoji ili je već opozvan.");
  });
});

describe("the token sign-in switch", () => {
  it("is on by default, and SHFD_GUI_TOKEN_LOGIN=0 (tokenLogin: false) turns it off", async () => {
    const on = await setup();
    on.fake.add("tok-admin", "admin");
    expect((await on.app.inject({ method: "GET", url: "/gui/api/config" })).json()).toEqual({ token_login: true });
    const t = on.fake.members.at(-1)!.token;
    const ok = await on.app.inject({ method: "POST", url: "/gui/api/login", payload: { token: t } });
    expect(ok.statusCode).toBe(200);
    expect(ok.json()).toMatchObject({ kind: "token", role: "admin", username: "tok-admin" });
    // A token session has no account.
    const cookie = String(ok.headers["set-cookie"]).split(";")[0];
    expect((await on.app.inject({ method: "GET", url: "/gui/api/me", headers: { cookie } })).statusCode).toBe(400);
    await gui!.app.close();

    const off = await setup({ tokenLogin: false });
    off.fake.add("tok-admin", "admin");
    expect((await off.app.inject({ method: "GET", url: "/gui/api/config" })).json()).toEqual({ token_login: false });
    const no = await off.app.inject({ method: "POST", url: "/gui/api/login", payload: { token: off.fake.members.at(-1)!.token } });
    expect(no.statusCode).toBe(404);
    expect(no.json().code).toBe("token_login_off");
    expect(no.headers["set-cookie"]).toBeUndefined();
    // The password sign-in is not affected.
    expect((await signIn(off.app, "ana", "anina-lozinka-1")).res.statusCode).toBe(200);
  });
});
