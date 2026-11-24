import { afterEach, describe, expect, it } from "vitest";
import { buildGui, LOGIN_INVALID, LOGIN_NOT_ADMIN, type GuiApp } from "../src/app.js";
import { FakeShfd } from "./fake-shfd.js";
import { fromPage } from "./origin.js";

let gui: GuiApp | undefined;
afterEach(async () => {
  await gui?.app.close();
  gui = undefined;
});

async function setup() {
  const fake = new FakeShfd();
  gui = await buildGui({ shfdUrl: "http://shfd", base: "/gui", secureCookie: true, fetchFn: fake.fetch, pollMs: 60_000 });
  return { fake, app: fromPage(gui.app) };
}

async function login(app: GuiApp["app"], token: string) {
  const res = await app.inject({ method: "POST", url: "/gui/api/login", payload: { token } });
  const cookie = String(res.headers["set-cookie"] ?? "").split(";")[0];
  return { res, cookie };
}

async function udc(app: GuiApp["app"], cookie: string, action: string, payload: unknown) {
  const res = await app.inject({ method: "POST", url: `/gui/udc/${action}`, payload: payload as object, headers: { cookie } });
  return res.json() as { kind: string; payload: any };
}

describe("sign-in", () => {
  it("an admin token signs in with an httpOnly, Secure, SameSite=Strict cookie", async () => {
    const { fake, app } = await setup();
    const { res, cookie } = await login(app, fake.members[0].token);
    expect(res.statusCode).toBe(200);
    expect(res.json()).toMatchObject({ kind: "token", share_id: fake.share, share_name: "projekti", username: "ana", role: "admin" });
    const set = String(res.headers["set-cookie"]);
    expect(set).toMatch(/HttpOnly/);
    expect(set).toMatch(/SameSite=Strict/);
    expect(set).toMatch(/Secure/);
    expect(set).toMatch(/Path=\/gui\//);
    expect(set).not.toContain(fake.members[0].token);
    expect(res.body).not.toContain(fake.members[0].token);
    const s = await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } });
    expect(s.json().username).toBe("ana");
  });

  it("a member token is refused and gets no session", async () => {
    const { fake, app } = await setup();
    const { res } = await login(app, fake.members[1].token);
    expect(res.statusCode).toBe(403);
    expect(res.json().error).toBe(LOGIN_NOT_ADMIN);
    expect(res.headers["set-cookie"]).toBeUndefined();
    expect(gui!.sessions.size()).toBe(0);
  });

  it("a wrong token gets the 401 message", async () => {
    const { app } = await setup();
    const { res } = await login(app, "shf_pogresan");
    expect(res.statusCode).toBe(401);
    expect(res.json().error).toBe(LOGIN_INVALID);
    const empty = await login(app, "  ");
    expect(empty.res.statusCode).toBe(400);
  });

  it("logout ends the session", async () => {
    const { fake, app } = await setup();
    const { cookie } = await login(app, fake.members[0].token);
    await app.inject({ method: "POST", url: "/gui/api/logout", headers: { cookie } });
    const s = await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } });
    expect(s.statusCode).toBe(401);
    const l = await udc(app, cookie, "list", { collection: "members" });
    expect(l.kind).toBe("udc.list.err");
    expect(l.payload.details.reason).toBe("unauthenticated");
  });
});

describe("collections and actions", () => {
  it("lists members without the token or the share id", async () => {
    const { fake, app } = await setup();
    const { cookie } = await login(app, fake.members[0].token);
    const l = await udc(app, cookie, "list", { collection: "members" });
    expect(l.kind).toBe("udc.list.ok");
    expect(l.payload.rows.map((r: any) => r.username)).toEqual(["ana", "bob"]);
    expect(JSON.stringify(l.payload)).not.toContain("shf_");
    expect(l.payload.rows[0].share_id).toBeUndefined();
  });

  it("adds a member and shows the token once; errors are sentences", async () => {
    const { fake, app } = await setup();
    const { cookie } = await login(app, fake.members[0].token);
    const add = await udc(app, cookie, "shfd.member_add", { username: "marko", admin: false });
    expect(add.kind).toBe("udc.shfd.member_add.ok");
    expect(add.payload.token).toBe(fake.members.find((m) => m.username === "marko")!.token);
    const dup = await udc(app, cookie, "shfd.member_add", { username: "marko", admin: false });
    expect(dup.kind).toBe("udc.shfd.member_add.err");
    expect(dup.payload.message).toBe("Član sa tim imenom već postoji.");
    const last = await udc(app, cookie, "shfd.member_set_role", { username: "ana", role: "member" });
    expect(last.payload.message).toMatch(/poslednji aktivni admin/);
    const rev = await udc(app, cookie, "shfd.member_revoke", { username: "marko" });
    expect(rev.payload).toEqual({ username: "marko", status: "revoked" });
    const back = await udc(app, cookie, "shfd.member_reactivate", { username: "marko" });
    expect(back.kind).toBe("udc.shfd.member_reactivate.ok");
  });

  it("an admin who gives up the role is signed out", async () => {
    const { fake, app } = await setup();
    fake.add("jelena", "admin");
    const { cookie } = await login(app, fake.members[0].token);
    const r = await udc(app, cookie, "shfd.member_set_role", { username: "ana", role: "member" });
    expect(r.kind).toBe("udc.shfd.member_set_role.ok");
    const s = await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } });
    expect(s.statusCode).toBe(401);
  });

  it("files: the root, a subfolder with .., and search inside it", async () => {
    const { fake, app } = await setup();
    const { cookie } = await login(app, fake.members[0].token);
    const root = await udc(app, cookie, "list", { collection: "files" });
    expect(root.payload.rows.map((r: any) => `${r.kind}:${r.name}`)).toEqual(["dir:docs", "file:a.txt"]);
    const sub = await udc(app, cookie, "list", { collection: "files", filter: { field: "path", op: "eq", value: "docs" } });
    expect(sub.payload.rows.map((r: any) => r.name)).toEqual(["..", "plan.md"]);
    expect(sub.payload.rows[0].path).toBe("");
    const found = await udc(app, cookie, "list", {
      collection: "files",
      filter: { and: [{ field: "path", op: "eq", value: "docs" }, { field: "name", op: "contains", value: "zzz" }] },
    });
    expect(found.payload.rows.map((r: any) => r.name)).toEqual([".."]);
  });

  it("renames a file in its folder through shfd's move op, with the version shown", async () => {
    const { fake, app } = await setup();
    const { cookie } = await login(app, fake.members[0].token);
    const plan = "bb".repeat(32);
    const r = await udc(app, cookie, "shfd.file_rename", { path: "docs/plan.md", name: " plan-v2.md ", base_hash: plan });
    expect(r.kind).toBe("udc.shfd.file_rename.ok");
    expect(r.payload).toEqual({ path: "docs/plan-v2.md", status: "applied", message: "Preimenovano u plan-v2.md." });
    expect(fake.ops.at(-1)).toEqual({ op: "move", path: "docs/plan.md", new_path: "docs/plan-v2.md", base_hash: plan });
    const sub = await udc(app, cookie, "list", { collection: "files", filter: { field: "path", op: "eq", value: "docs" } });
    expect(sub.payload.rows.map((r: any) => r.name)).toEqual(["..", "plan-v2.md"]);
    // A version changed since, a taken name, a bad name: sentences, and no op for the bad name.
    const stale = await udc(app, cookie, "shfd.file_rename", { path: "a.txt", name: "b.txt", base_hash: "00".repeat(32) });
    expect(stale.kind).toBe("udc.shfd.file_rename.err");
    expect(stale.payload.message).toMatch(/u međuvremenu izmenjen/);
    const taken = await udc(app, cookie, "shfd.file_rename", { path: "a.txt", name: "docs", base_hash: "aa".repeat(32) });
    expect(taken.payload.message).toBe("Na toj putanji već postoji drugi fajl.");
    const n = fake.ops.length;
    const bad = await udc(app, cookie, "shfd.file_rename", { path: "a.txt", name: "x/y", base_hash: "aa".repeat(32) });
    expect(bad.kind).toBe("udc.shfd.file_rename.err");
    expect(bad.payload.message).toMatch(/ne sme da sadrži/);
    const same = await udc(app, cookie, "shfd.file_rename", { path: "a.txt", name: "a.txt", base_hash: "aa".repeat(32) });
    expect(same.payload.status).toBe("unchanged");
    expect(fake.ops.length).toBe(n);
  });

  it("removes a file (to the trash) and an empty folder; a full folder is refused", async () => {
    const { fake, app } = await setup();
    const { cookie } = await login(app, fake.members[0].token);
    const full = await udc(app, cookie, "shfd.file_remove", { path: "docs", kind: "dir", base_hash: "" });
    expect(full.kind).toBe("udc.shfd.file_remove.err");
    expect(full.payload.message).toBe("Folder nije prazan; prvo obriši fajlove u njemu.");
    const f = await udc(app, cookie, "shfd.file_remove", { path: "docs/plan.md", kind: "file", base_hash: "bb".repeat(32) });
    expect(f.payload).toEqual({ path: "docs/plan.md", status: "applied", message: "plan.md je obrisan; može se vratiti iz Trash-a." });
    expect(fake.ops.at(-1)).toEqual({ op: "delete", path: "docs/plan.md", base_hash: "bb".repeat(32) });
    const d = await udc(app, cookie, "shfd.file_remove", { path: "docs", kind: "dir", base_hash: "" });
    expect(d.payload.message).toBe("Folder docs je obrisan.");
    expect(fake.ops.at(-1)).toEqual({ op: "rmdir", path: "docs" });
    const root = await udc(app, cookie, "list", { collection: "files" });
    expect(root.payload.rows.map((r: any) => r.name)).toEqual(["a.txt"]);
    const again = await udc(app, cookie, "shfd.file_remove", { path: "docs/plan.md", kind: "file", base_hash: "bb".repeat(32) });
    expect(again.payload.message).toBe("plan.md već ne postoji.");
  });

  it("a member's session cannot rename or remove", async () => {
    const { fake, app } = await setup();
    const r = await udc(app, "", "shfd.file_remove", { path: "a.txt", kind: "file", base_hash: "aa".repeat(32) });
    expect(r.kind).toMatch(/err$/);
    expect(fake.ops).toEqual([]);
  });

  it("logs come newest first from the buffer", async () => {
    const { fake, app } = await setup();
    fake.append({ ts: "2026-11-24T10:00:00Z", type: "event", seq: 1, member: "ana", op: "put", path: "a.txt" });
    fake.append({ ts: "2026-11-24T10:00:01Z", type: "event", seq: 2, member: "bob", op: "mkdir", path: "docs" });
    const { cookie } = await login(app, fake.members[0].token);
    const l = await udc(app, cookie, "list", { collection: "logs" });
    expect(l.payload.rows.map((r: any) => r.path)).toEqual(["docs", "a.txt"]);
    const ov = await udc(app, cookie, "shfd.overview", {});
    expect(ov.payload).toMatchObject({ version: "abc1234", server_ok: true, head: 3, members: 2, connected: 0 });
  });
});

describe("change stream", () => {
  it("asks proxies not to buffer it", async () => {
    const { fake, app } = await setup();
    const { cookie } = await login(app, fake.members[0].token);
    await app.listen({ port: 0, host: "127.0.0.1" });
    const addr = app.server.address() as { port: number };
    const ctl = new AbortController();
    const res = await fetch(`http://127.0.0.1:${addr.port}/gui/udc/changes?collections=logs`, { headers: { cookie }, signal: ctl.signal });
    expect(res.status).toBe(200);
    expect(res.headers.get("x-accel-buffering")).toBe("no");
    expect(res.headers.get("content-type")).toMatch(/event-stream/);
    ctl.abort();
  });
});

describe("thumbnails", () => {
  const H = "cd".repeat(32);
  const JPEG = new Uint8Array([0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 0xff, 0xd9]);

  it("files rows carry the thumbnail state; folders and .. have none", async () => {
    const { fake, app } = await setup();
    fake.files[""].push(
      { path: "slika.jpg", name: "slika.jpg", kind: "file", size: 9, mtime: "2026-11-24T10:00:00Z", hash: H, seq: 4, updated_by: "ana", thumb: "ready" },
      { path: "nova.pdf", name: "nova.pdf", kind: "file", size: 9, mtime: "2026-11-24T10:00:00Z", hash: "ef".repeat(32), seq: 5, updated_by: "ana", thumb: "pending" },
      { path: "stara.png", name: "stara.png", kind: "file", size: 9, mtime: "2026-11-24T10:00:00Z", hash: "01".repeat(32), seq: 6, updated_by: "ana" },
    );
    const { cookie } = await login(app, fake.members[0].token);
    const root = await udc(app, cookie, "list", { collection: "files" });
    const by = Object.fromEntries(root.payload.rows.map((r: any) => [r.name, r]));
    expect(by["slika.jpg"]).toMatchObject({ thumb: "ready", hash_full: H });
    expect(by["nova.pdf"].thumb).toBe("pending");
    expect(by["stara.png"].thumb).toBe(""); // an older shfd sends no thumb
    expect(by["docs"].thumb).toBe("");
    const sub = await udc(app, cookie, "list", { collection: "files", filter: { field: "path", op: "eq", value: "docs" } });
    expect(sub.payload.rows[0]).toMatchObject({ name: "..", thumb: "" });
  });

  it("the files view offers the Grid and extensions in its chip", async () => {
    const { fake, app } = await setup();
    const { cookie } = await login(app, fake.members[0].token);
    const d = await udc(app, cookie, "describe", { collection: "files" });
    expect(d.payload.view.available_renderers).toEqual(["table", "thumbs"]);
    const ext = d.payload.view.filters.find((f: any) => f.field === "ext");
    expect(ext.options).toEqual(expect.arrayContaining(["jpg", "pdf", "docx", "xlsx", "zip", "txt"]));
    const members = await udc(app, cookie, "describe", { collection: "members" });
    expect(members.payload.view.available_renderers).toEqual(["table"]);
  });

  it("needs a session", async () => {
    const { fake, app } = await setup();
    fake.thumbs.set(H, JPEG);
    const res = await app.inject({ method: "GET", url: `/gui/api/thumb/${H}?size=s` });
    expect(res.statusCode).toBe(401);
    expect(fake.calls.some((c) => c.includes("/thumbs/"))).toBe(false);
  });

  it("refuses a bad hash or size without asking shfd", async () => {
    const { fake, app } = await setup();
    const { cookie } = await login(app, fake.members[0].token);
    for (const url of [
      `/gui/api/thumb/${H.toUpperCase()}?size=s`,
      `/gui/api/thumb/${H.slice(1)}?size=s`,
      `/gui/api/thumb/..%2F${H.slice(3)}?size=s`,
      `/gui/api/thumb/${H}?size=xl`,
      `/gui/api/thumb/${H}?size=`,
    ]) {
      const res = await app.inject({ method: "GET", url, headers: { cookie } });
      expect(res.statusCode, url).toBe(400);
    }
    expect(fake.calls.some((c) => c.includes("/thumbs/"))).toBe(false);
  });

  it("sends the bytes with shfd's headers, a 304 for a known ETag and a 404 for none", async () => {
    const { fake, app } = await setup();
    fake.thumbs.set(H, JPEG);
    const { cookie } = await login(app, fake.members[0].token);
    const ok = await app.inject({ method: "GET", url: `/gui/api/thumb/${H}?size=l`, headers: { cookie } });
    expect(ok.statusCode).toBe(200);
    expect(ok.headers["content-type"]).toBe("image/jpeg");
    expect(ok.headers["etag"]).toBe(`"${H}.l"`);
    expect(ok.headers["cache-control"]).toBe("private, max-age=31536000, immutable");
    expect(new Uint8Array(ok.rawPayload)).toEqual(JPEG);
    expect(fake.calls).toContain(`GET /v1/shares/${fake.share}/thumbs/${H}?size=l`);

    const again = await app.inject({ method: "GET", url: `/gui/api/thumb/${H}?size=l`, headers: { cookie, "if-none-match": `"${H}.l"` } });
    expect(fake.lastIfNoneMatch).toBe(`"${H}.l"`);
    expect(again.statusCode).toBe(304);
    expect(again.headers["etag"]).toBe(`"${H}.l"`);
    expect(again.rawPayload.length).toBe(0);

    const none = await app.inject({ method: "GET", url: `/gui/api/thumb/${"ab".repeat(32)}?size=m`, headers: { cookie } });
    expect(none.statusCode).toBe(404);
    expect(none.headers["cache-control"]).toBe("no-store");
  });

  it("a 401 of shfd ends the session", async () => {
    const { fake, app } = await setup();
    fake.thumbs.set(H, JPEG);
    const { cookie } = await login(app, fake.members[0].token);
    fake.members[0].status = "revoked";
    const res = await app.inject({ method: "GET", url: `/gui/api/thumb/${H}?size=m`, headers: { cookie } });
    expect(res.statusCode).toBe(401);
    const s = await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } });
    expect(s.statusCode).toBe(401);
  });
});

describe("file content", () => {
  const TEXT = new TextEncoder().encode("zdravo, svete\n");
  const url = (path: string, extra = "") => `/gui/api/files/content?path=${encodeURIComponent(path)}${extra}`;

  it("needs a session and a path", async () => {
    const { fake, app } = await setup();
    fake.contents.set("a.txt", TEXT);
    const res = await app.inject({ method: "GET", url: url("a.txt") });
    expect(res.statusCode).toBe(401);
    expect(fake.calls.some((c) => c.includes("/files/content"))).toBe(false);
    const { cookie } = await login(app, fake.members[0].token);
    const none = await app.inject({ method: "GET", url: "/gui/api/files/content", headers: { cookie } });
    expect(none.statusCode).toBe(400);
  });

  it("passes shfd's headers and only those, and the bytes", async () => {
    const { fake, app } = await setup();
    fake.contents.set("docs/a b.txt", TEXT);
    const { cookie } = await login(app, fake.members[0].token);
    const res = await app.inject({ method: "GET", url: url("docs/a b.txt"), headers: { cookie } });
    expect(res.statusCode).toBe(200);
    expect(res.headers).toMatchObject({
      "content-type": "text/plain; charset=utf-8",
      "content-disposition": 'inline; filename="a b.txt"',
      "content-length": String(TEXT.length),
      "accept-ranges": "bytes",
      etag: `"${"cc".repeat(32)}"`,
      "cache-control": "private, no-cache",
      "content-security-policy": "sandbox",
      "x-content-type-options": "nosniff",
    });
    expect(res.headers["x-internal"]).toBeUndefined();
    expect(new Uint8Array(res.rawPayload)).toEqual(TEXT);
    expect(fake.calls).toContain(`GET /v1/shares/${fake.share}/files/content?path=docs%2Fa+b.txt`);
    expect(fake.lastContentHeaders?.get("authorization")).toBe(`Bearer ${fake.members[0].token}`);
    // The browser's cookie never reaches shfd.
    expect(fake.lastContentHeaders?.get("cookie")).toBeNull();
  });

  it("download=1, Range and If-None-Match reach shfd", async () => {
    const { fake, app } = await setup();
    fake.contents.set("a.txt", TEXT);
    fake.contents.set("noi.html", new TextEncoder().encode("<script>x</script>"));
    const { cookie } = await login(app, fake.members[0].token);
    const dl = await app.inject({ method: "GET", url: url("a.txt", "&download=1"), headers: { cookie } });
    expect(dl.headers["content-disposition"]).toBe('attachment; filename="a.txt"');
    expect(fake.calls.at(-1)).toBe(`GET /v1/shares/${fake.share}/files/content?path=a.txt&download=1`);
    const html = await app.inject({ method: "GET", url: url("noi.html"), headers: { cookie } });
    expect(html.headers["content-type"]).toBe("application/octet-stream");
    expect(html.headers["content-disposition"]).toBe('attachment; filename="noi.html"');

    const part = await app.inject({ method: "GET", url: url("a.txt"), headers: { cookie, range: "bytes=0-6" } });
    expect(part.statusCode).toBe(206);
    expect(part.headers["content-range"]).toBe(`bytes 0-6/${TEXT.length}`);
    expect(part.payload).toBe("zdravo,");

    const same = await app.inject({ method: "GET", url: url("a.txt"), headers: { cookie, "if-none-match": `"${"cc".repeat(32)}"` } });
    expect(same.statusCode).toBe(304);
    expect(same.rawPayload.length).toBe(0);
  });

  it("shfd's refusals come back with their status and a sentence", async () => {
    const { fake, app } = await setup();
    const { cookie } = await login(app, fake.members[0].token);
    const changed = await app.inject({ method: "GET", url: url("changed.txt"), headers: { cookie } });
    expect(changed.statusCode).toBe(409);
    expect(changed.json()).toMatchObject({ code: "file_changed" });
    expect(changed.json().error).toMatch(/verify/);
    const missing = await app.inject({ method: "GET", url: url("nema.txt"), headers: { cookie } });
    expect(missing.statusCode).toBe(404);
    const bad = await app.inject({ method: "GET", url: url("../x"), headers: { cookie } });
    expect(bad.statusCode).toBe(400);
  });

  it("a 401 of shfd ends the session", async () => {
    const { fake, app } = await setup();
    fake.contents.set("a.txt", TEXT);
    const { cookie } = await login(app, fake.members[0].token);
    fake.members[0].status = "revoked";
    const res = await app.inject({ method: "GET", url: url("a.txt"), headers: { cookie } });
    expect(res.statusCode).toBe(401);
    const s = await app.inject({ method: "GET", url: "/gui/api/session", headers: { cookie } });
    expect(s.statusCode).toBe(401);
  });

  it("streams: the first bytes arrive before shfd has sent the rest, and leaving stops shfd", async () => {
    const { fake, app } = await setup();
    let release!: () => void;
    const held = new Promise<void>((r) => (release = r));
    let canceled = false;
    const chunk = new Uint8Array(64 * 1024).fill(65);
    fake.contents.set("big.bin", () =>
      new ReadableStream<Uint8Array>({
        async start(c) {
          c.enqueue(chunk);
          await held; // the rest only once the test says so
          if (!canceled) c.enqueue(chunk);
          if (!canceled) c.close();
        },
        cancel() {
          canceled = true;
        },
      }),
    );
    const { cookie } = await login(app, fake.members[0].token);
    const addr = await app.listen({ port: 0, host: "127.0.0.1" });

    const res = await fetch(`${addr}${url("big.bin")}`, { headers: { cookie } });
    expect(res.status).toBe(200);
    expect(res.headers.get("content-disposition")).toBe('attachment; filename="big.bin"');
    const reader = res.body!.getReader();
    let got = 0;
    while (got < chunk.length) {
      const { value, done } = await reader.read();
      if (done) break;
      got += value.length;
    }
    expect(got).toBe(chunk.length); // all of the first chunk, while shfd still holds the second
    await reader.cancel();
    await new Promise((r) => setTimeout(r, 100));
    release();
    await new Promise((r) => setTimeout(r, 50));
    expect(canceled).toBe(true);
    app.server.closeAllConnections(); // fetch keeps the socket alive
  });
});
