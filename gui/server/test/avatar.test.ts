// Profile pictures through the GUI (TCK33XX9NH): the upload, the removal,
// the picture with shfd's caching headers, avatar_url in the session, Moj
// nalog, Članovi and Logovi.

import { afterEach, describe, expect, it } from "vitest";
import { buildGui, type GuiApp } from "../src/app.js";
import { AVATARS_TTL_MS, MemberAvatars } from "../src/collections.js";
import { ShfdClient } from "../src/shfd.js";
import { FakeShfd } from "./fake-shfd.js";
import { fromPage } from "./origin.js";

let gui: GuiApp | undefined;
afterEach(async () => {
  await gui?.app.close();
  gui = undefined;
});

const PNG = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 1, 2, 3, 4]);
const HTML = Buffer.from("<!doctype html><title>me.png</title>");

async function setup() {
  const fake = new FakeShfd();
  fake.members = [];
  fake.user("ana", "anina-lozinka-1", { displayName: "Ana Anić" });
  fake.user("bob", "bobova-lozinka-1");
  fake.addAccountMember("ana", "admin");
  fake.addAccountMember("bob", "member");
  gui = await buildGui({ shfdUrl: "http://shfd", base: "/gui", secureCookie: true, fetchFn: fake.fetch, pollMs: 60_000 });
  const app = fromPage(gui.app);
  const signIn = async (u: string, pw: string) => {
    const res = await app.inject({ method: "POST", url: "/gui/api/auth/login", payload: { username: u, password: pw } });
    return String(res.headers["set-cookie"] ?? "").split(";")[0];
  };
  return { fake, app, signIn };
}

const put = (app: GuiApp["app"], cookie: string, body: Buffer, type = "image/png") =>
  app.inject({ method: "PUT", url: "/gui/api/me/avatar", payload: body, headers: { cookie, "content-type": type } });

describe("profile picture", () => {
  it("uploads, shows in the session and Moj nalog, and is removed", async () => {
    const { fake, app, signIn } = await setup();
    const ana = await signIn("ana", "anina-lozinka-1");
    let s = await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie: ana } });
    expect(s.json().avatar_url).toBeNull();

    const res = await put(app, ana, PNG);
    expect(res.statusCode).toBe(200);
    expect(res.headers["cache-control"]).toBe("no-store");
    const url = res.json().avatar_url as string;
    expect(url).toMatch(/^\/v1\/users\/ana\/avatar\?v=\d+$/);
    expect(res.json().session.avatar_url).toBe(url);
    expect(fake.lastAvatarType).toBe("image/png");
    expect(Buffer.from(fake.avatars.get("ana")!.bytes)).toEqual(PNG);

    s = await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie: ana } });
    expect(s.json().avatar_url).toBe(url);
    const me = await app.inject({ method: "GET", url: "/gui/api/me", headers: { cookie: ana } });
    expect(me.json().user.avatar_url).toBe(url);

    const del = await app.inject({ method: "DELETE", url: "/gui/api/me/avatar", headers: { cookie: ana } });
    expect(del.statusCode).toBe(200);
    expect(del.json()).toMatchObject({ ok: true, avatar_url: null, session: { avatar_url: null } });
    expect(fake.avatars.has("ana")).toBe(false);
  });

  it("a new sign-in carries the picture it has", async () => {
    const { app, signIn } = await setup();
    await put(app, await signIn("bob", "bobova-lozinka-1"), PNG);
    const again = await signIn("bob", "bobova-lozinka-1");
    const s = await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie: again } });
    expect(s.json().avatar_url).toMatch(/^\/v1\/users\/bob\/avatar\?v=/);
  });

  it("passes shfd's refusals on as sentences, in Serbian and English", async () => {
    const { app, signIn } = await setup();
    const ana = await signIn("ana", "anina-lozinka-1");
    // An HTML page named .png: shfd reads the content and refuses it.
    let res = await put(app, ana, HTML);
    expect(res.statusCode).toBe(415);
    expect(res.json()).toMatchObject({ code: "unsupported_image", error: expect.stringContaining("PNG, JPEG, WebP ili GIF") });
    res = await app.inject({ method: "PUT", url: "/gui/api/me/avatar", payload: HTML, headers: { cookie: ana, "content-type": "image/png", "x-shfd-lang": "en" } });
    expect(res.json().error).toContain("not a picture in a supported format");
    // Too large never reaches shfd.
    res = await put(app, ana, Buffer.concat([PNG, Buffer.alloc(5 * 1024 * 1024)]));
    expect(res.statusCode).toBe(413);
    expect(res.json()).toMatchObject({ code: "image_too_large", error: "Slika je prevelika; najviše 5 MB." });
    // A type that is not a picture at all.
    res = await put(app, ana, HTML, "text/html");
    expect(res.statusCode).toBe(415);
    expect(res.json().code).toBe("unsupported_image");
    // Nothing at all.
    res = await put(app, ana, Buffer.alloc(0));
    expect(res.statusCode).toBe(415);
  });

  it("needs a signed-in account to upload, and a session to see one", async () => {
    const { app } = await setup();
    expect((await put(app, "", PNG)).statusCode).toBe(401);
    const res = await app.inject({ method: "GET", url: "/gui/api/avatar/ana?v=1" });
    expect(res.statusCode).toBe(401);
    expect(res.headers["cache-control"]).toBe("no-store");
  });

  it("serves the picture with shfd's caching headers; 304 and 404 pass through", async () => {
    const { fake, app, signIn } = await setup();
    const ana = await signIn("ana", "anina-lozinka-1");
    const bob = await signIn("bob", "bobova-lozinka-1");
    const url = (await put(app, ana, PNG)).json().avatar_url as string;
    const v = new URL(url, "http://x").searchParams.get("v");

    // bob sees ana's picture: by its version for good, otherwise revalidated.
    let res = await app.inject({ method: "GET", url: `/gui/api/avatar/ana?v=${v}`, headers: { cookie: bob } });
    expect(res.statusCode).toBe(200);
    expect(res.headers["cache-control"]).toBe("private, max-age=31536000, immutable");
    expect(res.headers["content-type"]).toBe("image/jpeg");
    expect(res.headers["x-content-type-options"]).toBe("nosniff");
    expect(res.rawPayload).toEqual(PNG);
    const etag = String(res.headers.etag);
    res = await app.inject({ method: "GET", url: "/gui/api/avatar/ana?v=1", headers: { cookie: bob } });
    expect(res.headers["cache-control"]).toBe("private, no-cache");
    res = await app.inject({ method: "GET", url: `/gui/api/avatar/ana?v=${v}`, headers: { cookie: bob, "if-none-match": etag } });
    expect(res.statusCode).toBe(304);
    expect(fake.calls).toContain(`GET /v1/users/ana/avatar?v=${v}`);

    res = await app.inject({ method: "GET", url: "/gui/api/avatar/bob?v=1", headers: { cookie: ana } });
    expect(res.statusCode).toBe(404);
    expect(res.headers["cache-control"]).toBe("no-store");
    res = await app.inject({ method: "GET", url: "/gui/api/avatar/..%2Fetc?v=1", headers: { cookie: ana } });
    expect(res.statusCode).toBe(400);
  });

  it("Članovi and Logovi rows carry the picture of the member's account", async () => {
    const { fake, app, signIn } = await setup();
    const ana = await signIn("ana", "anina-lozinka-1");
    await app.inject({ method: "POST", url: "/gui/api/folder", payload: { share_id: fake.share }, headers: { cookie: ana } });
    const url = (await put(app, ana, PNG)).json().avatar_url;
    fake.append({ ts: "2026-11-24T12:00:00Z", type: "event", member: "ana", op: "put", path: "a.txt", seq: 1 } as never);
    fake.append({ ts: "2026-11-24T12:00:01Z", type: "event", member: "bob", op: "put", path: "b.txt", seq: 2 } as never);

    const list = async (collection: string) => {
      const r = await app.inject({ method: "POST", url: "/gui/udc/list", payload: { collection }, headers: { cookie: ana } });
      return r.json().payload.rows as Record<string, unknown>[];
    };
    const members = Object.fromEntries((await list("members")).map((r) => [r.username, r.avatar_url]));
    expect(members).toEqual({ ana: url, bob: null });
    const logs = await list("logs");
    expect(logs.find((r) => r.member === "ana")?.avatar_url).toBe(url);
    expect(logs.find((r) => r.member === "bob")?.avatar_url).toBeNull();
  });
});

describe("MemberAvatars", () => {
  it("asks shfd for the members at most once per TTL per folder, and survives a failure", async () => {
    const fake = new FakeShfd();
    fake.members = [];
    fake.user("ana", "x");
    fake.addAccountMember("ana", "admin");
    const tok = fake.members[0];
    tok.token = "shf_ana";
    fake.avatars.set("ana", { v: 7, bytes: PNG, type: "image/jpeg" });
    let t = 0;
    const pics = new MemberAvatars(new ShfdClient("http://shfd", fake.fetch), () => t);
    expect((await pics.get("shf_ana", fake.share)).get("ana")).toBe("/v1/users/ana/avatar?v=7");
    await pics.get("shf_ana", fake.share);
    expect(fake.calls.filter((c) => c.endsWith("/admin/members")).length).toBe(1);
    t += AVATARS_TTL_MS;
    fake.avatars.set("ana", { v: 8, bytes: PNG, type: "image/jpeg" });
    expect((await pics.get("shf_ana", fake.share)).get("ana")).toBe("/v1/users/ana/avatar?v=8");
    // shfd refuses: the last map stands.
    t += AVATARS_TTL_MS;
    expect((await pics.get("bad", fake.share)).get("ana")).toBe("/v1/users/ana/avatar?v=8");
  });
});
