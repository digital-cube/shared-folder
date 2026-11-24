import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { isShfdPath, normalizeBase } from "../../shared/base.js";
import { buildGui, withBase, type GuiApp } from "../src/app.js";
import { COOKIE } from "../src/sessions.js";
import { FakeShfd } from "./fake-shfd.js";

// One build of the web app, served at the root of the host and under /gui.

const INDEX = `<!doctype html>
<html>
  <head>
    <meta charset="utf-8" />
    <script type="module" crossorigin src="./assets/index-abc.js"></script>
  </head>
  <body><div id="root"></div></body>
</html>
`;

let web: string;
beforeAll(() => {
  web = mkdtempSync(join(tmpdir(), "shfd-gui-web-"));
  mkdirSync(join(web, "assets"));
  writeFileSync(join(web, "index.html"), INDEX);
  writeFileSync(join(web, "assets", "index-abc.js"), "console.log(1)");
});
afterAll(() => rmSync(web, { recursive: true, force: true }));

let gui: GuiApp | undefined;
afterEach(async () => {
  await gui?.app.close();
  gui = undefined;
});

async function setup(base: string, now?: () => number) {
  const fake = new FakeShfd();
  gui = await buildGui({
    shfdUrl: "http://shfd",
    base,
    version: "v1.2.3",
    webDir: web,
    secureCookie: true,
    fetchFn: fake.fetch,
    pollMs: 60_000,
    now,
  });
  return { fake, app: gui.app };
}

describe("normalizeBase", () => {
  it("the root is the empty string, a prefix has no trailing slash", () => {
    for (const r of ["", "/", "//", "  ", undefined, null]) expect(normalizeBase(r), String(r)).toBe("");
    for (const r of ["/gui", "/gui/", "gui", "gui/"]) expect(normalizeBase(r), r).toBe("/gui");
    expect(normalizeBase("/a/b/")).toBe("/a/b");
  });
});

describe("isShfdPath", () => {
  it("shfd's paths, and nothing of the GUI's", () => {
    for (const p of ["/v1", "/v1/", "/v1/shares/x/ws", "/v1/downloads", "/healthz", "/healthz?x=1", "/downloads/", "/downloads/SHA256SUMS"]) {
      expect(isShfdPath(p), p).toBe(true);
    }
    for (const p of ["/", "/api/healthz", "/udc/changes", "/assets/a.js", "/v1x", "/healthzz", "/downloadsx", "/gui/healthz"]) {
      expect(isShfdPath(p), p).toBe(false);
    }
  });
});

describe("withBase", () => {
  it("writes the base href and the meta right after <head>", () => {
    expect(withBase(INDEX, "")).toContain('<head>\n    <base href="/" />\n    <meta name="shfd-gui-base" content="" />');
    expect(withBase(INDEX, "/gui")).toContain('<base href="/gui/" />\n    <meta name="shfd-gui-base" content="/gui" />');
    expect(withBase(INDEX, '/a"<b')).toContain('content="/a&quot;&lt;b"');
  });
});

describe.each([
  { base: "", label: "root" },
  { base: "/gui", label: "/gui" },
])("the GUI at $label", ({ base }) => {
  it("serves index.html with its base at <base>/", async () => {
    const { app } = await setup(base);
    const r = await app.inject({ method: "GET", url: `${base}/` });
    expect(r.statusCode).toBe(200);
    expect(r.headers["content-type"]).toMatch(/text\/html/);
    expect(r.body).toContain(`<meta name="shfd-gui-base" content="${base}" />`);
    expect(r.body).toContain(`<base href="${base}/" />`);
    expect(r.body).toContain('src="./assets/index-abc.js"');
    const a = await app.inject({ method: "GET", url: `${base}/assets/index-abc.js` });
    expect(a.statusCode).toBe(200);
    expect(a.headers["cache-control"]).toMatch(/immutable/);
  });

  it("answers its health at <base>/api/healthz", async () => {
    const { app } = await setup(base);
    const r = await app.inject({ method: "GET", url: `${base}/api/healthz` });
    expect(r.statusCode).toBe(200);
    expect(r.json()).toEqual({ service: "shfd-gui", version: "v1.2.3", ok: true });
  });

  it("the cookie's Path follows the base; logout clears that cookie", async () => {
    const { fake, app } = await setup(base);
    const res = await app.inject({ method: "POST", url: `${base}/api/login`, payload: { token: fake.members[0].token } });
    expect(res.statusCode).toBe(200);
    const set = String(res.headers["set-cookie"]);
    expect(set).toContain(`Path=${base}/;`);
    const cookie = set.split(";")[0];
    const s = await app.inject({ method: "GET", url: `${base}/api/session`, headers: { cookie } });
    expect(s.json().username).toBe("ana");
    const l = (await app.inject({ method: "POST", url: `${base}/udc/list`, payload: { collection: "members" }, headers: { cookie } })).json();
    expect(l.kind).toBe("udc.list.ok");
    const out = await app.inject({ method: "POST", url: `${base}/api/logout`, headers: { cookie } });
    expect(String(out.headers["set-cookie"])).toBe(`${COOKIE}=; Path=${base}/; HttpOnly; SameSite=Strict; Secure; Max-Age=0`);
    expect((await app.inject({ method: "GET", url: `${base}/api/session`, headers: { cookie } })).statusCode).toBe(401);
  });

  it("an expired session is unauthenticated", async () => {
    let t = 1_000_000;
    const { fake, app } = await setup(base, () => t);
    const res = await app.inject({ method: "POST", url: `${base}/api/login`, payload: { token: fake.members[0].token } });
    const cookie = String(res.headers["set-cookie"]).split(";")[0];
    t += 13 * 60 * 60 * 1000;
    expect((await app.inject({ method: "GET", url: `${base}/api/session`, headers: { cookie } })).statusCode).toBe(401);
    const l = (await app.inject({ method: "POST", url: `${base}/udc/list`, payload: { collection: "members" }, headers: { cookie } })).json();
    expect(l.payload.details.reason).toBe("unauthenticated");
  });
});

describe("the GUI at the root of the host", () => {
  it("neither serves nor hides shfd's paths", async () => {
    const { app } = await setup("");
    for (const url of ["/v1/shares", "/v1/shares/abc/ws", "/v1/downloads", "/v1", "/healthz", "/downloads/", "/downloads/SHA256SUMS"]) {
      const r = await app.inject({ method: "GET", url });
      expect(r.statusCode, url).toBe(404);
      expect(r.body, url).not.toContain("<html");
    }
    const p = await app.inject({ method: "POST", url: "/v1/shares/abc/members", payload: {} });
    expect(p.statusCode).toBe(404);
  });

  it("has no /gui of its own (nginx redirects old bookmarks)", async () => {
    const { app } = await setup("");
    const r = await app.inject({ method: "GET", url: "/api/healthz" });
    expect(r.statusCode).toBe(200);
    const l = await app.inject({ method: "POST", url: "/gui/api/login", payload: { token: "x" } });
    expect(l.statusCode).toBe(404);
  });

  it("the change stream is unbuffered at /udc/changes", async () => {
    const { fake, app } = await setup("");
    const res = await app.inject({ method: "POST", url: "/api/login", payload: { token: fake.members[0].token } });
    const cookie = String(res.headers["set-cookie"]).split(";")[0];
    await app.listen({ port: 0, host: "127.0.0.1" });
    const addr = app.server.address() as { port: number };
    const ctl = new AbortController();
    const r = await fetch(`http://127.0.0.1:${addr.port}/udc/changes?collections=logs`, { headers: { cookie }, signal: ctl.signal });
    expect(r.status).toBe(200);
    expect(r.headers.get("content-type")).toMatch(/text\/event-stream/);
    expect(r.headers.get("x-accel-buffering")).toBe("no");
    ctl.abort();
  });
});

describe("the GUI under /gui", () => {
  it("keeps /gui/healthz as an alias and redirects /gui to /gui/", async () => {
    const { app } = await setup("/gui");
    const h = await app.inject({ method: "GET", url: "/gui/healthz" });
    expect(h.statusCode).toBe(200);
    expect(h.json().ok).toBe(true);
    const r = await app.inject({ method: "GET", url: "/gui" });
    expect(r.statusCode).toBe(302);
    expect(r.headers.location).toBe("/gui/");
    expect((await app.inject({ method: "GET", url: "/" })).statusCode).toBe(404);
  });

  it("an empty base given as \"/\" is the root", async () => {
    const { app } = await setup("/");
    const r = await app.inject({ method: "GET", url: "/" });
    expect(r.body).toContain('<meta name="shfd-gui-base" content="" />');
  });
});
