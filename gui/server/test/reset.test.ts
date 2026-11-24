// A forgotten password and the account's email (TCKP7URK2X): the forgot
// form, the reset page's call, the email in "Moj nalog", and the client
// address passed on to shfd as a proxy would.

import { afterEach, describe, expect, it } from "vitest";
import { forgotSent, tooManyRequests } from "../src/account.js";
import { buildGui, type GuiApp } from "../src/app.js";
import { forwardedFor } from "../src/shfd.js";
import { FakeShfd } from "./fake-shfd.js";
import { bare, fromPage } from "./origin.js";

let gui: GuiApp | undefined;
afterEach(async () => {
  await gui?.app.close();
  gui = undefined;
});

async function setup() {
  const fake = new FakeShfd();
  fake.members = [];
  fake.user("ana", "anina-lozinka-1", { email: "ana@example.com" });
  fake.user("bob", "bobova-lozinka-1");
  fake.addAccountMember("ana", "admin");
  gui = await buildGui({ shfdUrl: "http://shfd", base: "/gui", secureCookie: true, fetchFn: fake.fetch, pollMs: 60_000 });
  return { fake, app: fromPage(gui.app) };
}

type App = GuiApp["app"];

const forgot = (app: App, login: unknown, headers: Record<string, string> = {}) =>
  app.inject({ method: "POST", url: "/gui/api/auth/forgot", payload: { login }, headers });
const reset = (app: App, token: unknown, pw: unknown, headers: Record<string, string> = {}) =>
  app.inject({ method: "POST", url: "/gui/api/auth/reset", payload: { token, new_password: pw }, headers });

async function signIn(app: App, username: string, password: string) {
  const res = await app.inject({ method: "POST", url: "/gui/api/auth/login", payload: { username, password } });
  return String(res.headers["set-cookie"] ?? "").split(";")[0];
}

describe("forgotten password: the form", () => {
  it("answers the same for an account and for nothing, and only an account gets a mail", async () => {
    const { fake, app } = await setup();
    const answers = [];
    for (const login of ["ana", "ana@example.com", "nobody", "nobody@example.com", "bob"]) {
      const res = await forgot(app, login);
      expect(res.statusCode).toBe(202);
      expect(res.headers["cache-control"]).toBe("no-store");
      answers.push(res.body);
    }
    expect(new Set(answers).size).toBe(1);
    expect(JSON.parse(answers[0])).toEqual({ ok: true, message: forgotSent("sr") });
    // bob has no email: no mail; ana asked twice, the second link replaced the first.
    expect(fake.mails.map((m) => [m.to, m.kind, m.lang])).toEqual([
      ["ana@example.com", "reset", "sr"],
      ["ana@example.com", "reset", "sr"],
    ]);
    expect(fake.resetTokens.size).toBe(1);
  });

  it("asks for the mail in the UI's language and says the answer in it", async () => {
    const { fake, app } = await setup();
    const res = await forgot(app, "ana", { "x-shfd-lang": "en" });
    expect(res.json().message).toBe(forgotSent("en"));
    expect(res.json().message).toMatch(/^If the account exists/);
    expect(fake.mails[0].lang).toBe("en");
  });

  it("refuses an empty login, and passes on too many requests with the wait", async () => {
    const { fake, app } = await setup();
    const empty = await forgot(app, "  ");
    expect(empty.statusCode).toBe(400);
    expect(empty.json().error).toBe("Upiši korisničko ime ili email.");
    fake.forgotLimit = 0;
    const res = await forgot(app, "ana");
    expect(res.statusCode).toBe(429);
    expect(res.headers["retry-after"]).toBe("3600");
    expect(res.json()).toMatchObject({ code: "too_many_attempts", error: tooManyRequests("sr", 3600) });
    expect(res.json().error).toBe("Previše zahteva sa ove adrese. Pokušaj ponovo za 60 min.");
  });

  it("needs no session but keeps the CSRF check", async () => {
    const { app } = await setup();
    const res = await bare(app)({ method: "POST", url: "/gui/api/auth/forgot", payload: { login: "ana" } });
    expect(res.statusCode).toBe(403);
    expect(res.json().code).toBe("csrf");
  });

  it("passes the client's address to shfd as a proxy does, for forgot, reset and sign-in", async () => {
    const { fake, app } = await setup();
    // nginx wrote the client; the GUI adds the address it saw (nginx's).
    await forgot(app, "ana", { "x-forwarded-for": "203.0.113.7" });
    expect(fake.lastForwardedFor).toBe("203.0.113.7, 127.0.0.1");
    await reset(app, "nope", "nova-lozinka-123", { "x-forwarded-for": "203.0.113.8" });
    expect(fake.lastForwardedFor).toBe("203.0.113.8, 127.0.0.1");
    await app.inject({ method: "POST", url: "/gui/api/auth/login", payload: { username: "ana", password: "x" }, headers: { "x-forwarded-for": "198.51.100.1, 203.0.113.9" } });
    expect(fake.lastForwardedFor).toBe("198.51.100.1, 203.0.113.9, 127.0.0.1");
    // Reached directly: only the peer.
    await forgot(app, "ana");
    expect(fake.lastForwardedFor).toBe("127.0.0.1");
  });
});

describe("forgotten password: the reset page", () => {
  it("sets the new password, signs out the account's GUI sessions and names the account", async () => {
    const { fake, app } = await setup();
    const cookie = await signIn(app, "ana", "anina-lozinka-1");
    expect((await app.inject({ method: "GET", url: "/gui/api/me", headers: { cookie } })).statusCode).toBe(200);
    await forgot(app, "ana", { "x-shfd-lang": "en" });
    const token = fake.mails[0].token!;

    const short = await reset(app, token, "kratka");
    expect(short.statusCode).toBe(400);
    expect(short.json().error).toBe("Lozinka mora imati bar 12 znakova.");

    const res = await reset(app, token, "nova-anina-lozinka", { "x-shfd-lang": "en" });
    expect(res.statusCode).toBe(200);
    expect(res.json()).toEqual({ ok: true, username: "ana" });
    expect(fake.mails.at(-1)).toMatchObject({ to: "ana@example.com", kind: "changed", lang: "en" });
    // The browser's earlier session is gone with its shfd device.
    const me = await app.inject({ method: "GET", url: "/gui/api/me", headers: { cookie } });
    expect(me.statusCode).toBe(401);
    // The new password signs in.
    const r = await app.inject({ method: "POST", url: "/gui/api/auth/login", payload: { username: "ana", password: "nova-anina-lozinka" } });
    expect(r.statusCode).toBe(200);

    // The link works once.
    const again = await reset(app, token, "treca-anina-lozinka");
    expect(again.statusCode).toBe(400);
    expect(again.json()).toMatchObject({ code: "invalid_reset_token" });
    expect(again.json().error).toMatch(/^Link za novu lozinku nije ispravan/);
  });

  it("refuses an empty token or password before shfd", async () => {
    const { fake, app } = await setup();
    const calls = fake.calls.length;
    expect((await reset(app, "", "nova-anina-lozinka")).json().code).toBe("invalid_reset_token");
    const r = await reset(app, "rst_x", "", { "x-shfd-lang": "en" });
    expect(r.statusCode).toBe(400);
    expect(r.json().error).toBe("Enter the new password.");
    expect(fake.calls.length).toBe(calls);
  });
});

describe("Moj nalog: email", () => {
  it("shows the email and changes it with the password", async () => {
    const { fake, app } = await setup();
    const cookie = await signIn(app, "ana", "anina-lozinka-1");
    const me = await app.inject({ method: "GET", url: "/gui/api/me", headers: { cookie } });
    expect(me.json().user.email).toBe("ana@example.com");

    const put = (email: string, current_password: string, lang = "sr") =>
      app.inject({ method: "PUT", url: "/gui/api/me/email", payload: { email, current_password }, headers: { cookie, "x-shfd-lang": lang } });

    let r = await put("ana@novo.example.com", "");
    expect(r.statusCode).toBe(400);
    expect(r.json().error).toBe("Upiši svoju lozinku.");
    r = await put("ana@novo.example.com", "pogresna");
    expect(r.statusCode).toBe(403);
    expect(r.json().error).toBe("Lozinka nije tačna.");
    r = await put("ana", "anina-lozinka-1", "en");
    expect(r.statusCode).toBe(400);
    expect(r.json().error).toBe("That is not a valid email address.");
    fake.users[1].email = "bob@example.com";
    r = await put("BOB@example.com", "anina-lozinka-1");
    expect(r.statusCode).toBe(409);
    expect(r.json().error).toBe("Tu email adresu već ima drugi nalog.");
    r = await put(" Ana@Novo.example.com ", "anina-lozinka-1");
    expect(r.statusCode).toBe(200);
    expect(r.json()).toEqual({ ok: true, email: "ana@novo.example.com" });
    expect((await app.inject({ method: "GET", url: "/gui/api/me", headers: { cookie } })).json().user.email).toBe("ana@novo.example.com");
    // Removing it.
    r = await put("", "anina-lozinka-1");
    expect(r.json()).toEqual({ ok: true, email: "" });
  });

  it("is only for an account session", async () => {
    const { app } = await setup();
    const r = await app.inject({ method: "PUT", url: "/gui/api/me/email", payload: { email: "a@b.rs", current_password: "x" } });
    expect(r.statusCode).toBe(401);
  });
});

describe("forwardedFor", () => {
  it("appends the peer to what came, as nginx's $proxy_add_x_forwarded_for", () => {
    expect(forwardedFor({ headers: {}, socket: { remoteAddress: "127.0.0.1" } })).toBe("127.0.0.1");
    expect(forwardedFor({ headers: { "x-forwarded-for": " 203.0.113.1 " }, socket: { remoteAddress: "::ffff:127.0.0.1" } })).toBe("203.0.113.1, ::ffff:127.0.0.1");
    expect(forwardedFor({ headers: { "x-forwarded-for": ["1.1.1.1", "2.2.2.2"] }, socket: {} })).toBe("1.1.1.1, 2.2.2.2");
  });
});
