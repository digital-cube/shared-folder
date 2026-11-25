// A ZIP of the selection of the Files page: shfd.files_zip prepares it,
// GET <base>/api/files/zip streams it (TCKCFD3MM3). The archive is opened
// and read here (yauzl), over a real HTTP connection.

import { createHash } from "node:crypto";
import type { AddressInfo } from "node:net";
import { afterEach, describe, expect, it } from "vitest";
import yauzl from "yauzl";
import { buildGui, type GuiApp } from "../src/app.js";
import { planZip, ZipJobs } from "../src/zip.js";
import { attachmentDisposition, zipName, zipRequestProblem, ZIP_MAX_PATHS } from "../../shared/zip.js";
import type { FileEntry } from "../src/shfd.js";
import { FakeShfd } from "./fake-shfd.js";
import { fromPage } from "./origin.js";

let gui: GuiApp | undefined;
afterEach(async () => {
  // fetch (undici) may keep a spare connection that never sent a request,
  // which an HTTP server does not count as idle.
  gui?.app.server.closeAllConnections();
  await gui?.app.close();
  gui = undefined;
});

const OTHER = "6b1d0000-0000-4000-8000-000000000002";
const enc = (s: string) => new TextEncoder().encode(s);
const file = (path: string, size: number, mtime = "2026-11-24T10:00:00Z"): FileEntry => ({
  path,
  name: path.split("/").pop()!,
  kind: "file",
  size,
  mtime,
  hash: createHash("sha256").update(path).digest("hex"),
  seq: 1,
  updated_by: "ana",
  thumb: "",
});
const dir = (path: string): FileEntry => ({ path, name: path.split("/").pop()!, kind: "dir", size: 0, mtime: "2026-11-20T08:00:00Z", hash: "", seq: 1, updated_by: "ana", thumb: "" });

const CONTENT: Record<string, string> = {
  "a.txt": "abc",
  "Ćao šđž.txt": "zdravo",
  "docs/plan.md": "# plan\n",
  "docs/čćšžđ beleška.txt": "naša slova: čćšžđ ČĆŠŽĐ",
  "docs/slike/more na plaži.jpg": "\xff\xd8\xff jpeg bytes",
  "docs/slike/duboko/x.bin": "x".repeat(70_000),
};

/** ana: admin of "projekti" and a member of "arhiva"; bob: a member of "projekti". */
async function setup() {
  const fake = new FakeShfd();
  fake.members = [];
  fake.user("ana", "anina-lozinka-1");
  fake.user("bob", "bobova-lozinka-1");
  fake.addAccountMember("ana", "admin");
  fake.addAccountMember("bob", "member");
  fake.folders.push({ share_id: OTHER, name: "arhiva", members: { ana: "member" } });
  fake.files = {
    "": [dir("docs"), file("a.txt", 3), file("Ćao šđž.txt", 6)],
    docs: [file("docs/plan.md", 7), file("docs/čćšžđ beleška.txt", 30), dir("docs/slike")],
    "docs/slike": [file("docs/slike/more na plaži.jpg", 10), dir("docs/slike/prazno"), dir("docs/slike/duboko")],
    "docs/slike/prazno": [],
    "docs/slike/duboko": [file("docs/slike/duboko/x.bin", 70_000)],
  };
  for (const [p, c] of Object.entries(CONTENT)) fake.contents.set(p, enc(c));
  let t = Date.parse("2026-11-24T12:00:00Z");
  const clock = { now: () => t, advance: (ms: number) => (t += ms) };
  gui = await buildGui({ shfdUrl: "http://shfd", base: "/gui", secureCookie: false, fetchFn: (i, init) => fake.fetch(i, init), pollMs: 60_000, now: clock.now });
  const app = fromPage(gui.app);
  await gui.app.listen({ port: 0, host: "127.0.0.1" });
  const url = `http://127.0.0.1:${(gui.app.server.address() as AddressInfo).port}`;
  return { fake, app, clock, url };
}

type App = GuiApp["app"];

const SHARE = new FakeShfd().share;

async function signIn(app: App, username: string, password: string, share = SHARE) {
  const res = await app.inject({ method: "POST", url: "/gui/api/auth/login", payload: { username, password } });
  const cookie = String(res.headers["set-cookie"] ?? "").split(";")[0];
  await app.inject({ method: "POST", url: "/gui/api/folder", payload: { share_id: share }, headers: { cookie } });
  return cookie;
}

async function prepare(app: App, cookie: string, payload: unknown) {
  const res = await app.inject({ method: "POST", url: "/gui/udc/shfd.files_zip", payload: payload as object, headers: { cookie } });
  const r = res.json() as { kind: string; payload: any };
  return { ...r, ok: r.kind === "udc.shfd.files_zip.ok" };
}

const download = (url: string, cookie: string, id: string) => fetch(`${url}/gui/api/files/zip?id=${encodeURIComponent(id)}`, { headers: { cookie } });

interface Unzipped {
  name: string;
  dir: boolean;
  utf8: boolean;
  data: string;
  mtime: Date;
}

/** Every entry of the archive, read and checked (CRC) by yauzl. */
function unzip(buf: Buffer): Promise<Unzipped[]> {
  return new Promise((resolve, reject) => {
    yauzl.fromBuffer(buf, { lazyEntries: true, validateEntrySizes: true }, (err, zf) => {
      if (err || !zf) return reject(err);
      const out: Unzipped[] = [];
      zf.on("error", reject);
      zf.on("end", () => resolve(out));
      zf.on("entry", (e: yauzl.Entry) => {
        const base = { name: e.fileName, dir: e.fileName.endsWith("/"), utf8: (e.generalPurposeBitFlag & 0x800) !== 0, mtime: e.getLastModDate() };
        if (base.dir) {
          out.push({ ...base, data: "" });
          zf.readEntry();
          return;
        }
        zf.openReadStream(e, (err2, rs) => {
          if (err2 || !rs) return reject(err2);
          const chunks: Buffer[] = [];
          rs.on("data", (c: Buffer) => chunks.push(c));
          rs.on("error", reject);
          rs.on("end", () => {
            out.push({ ...base, data: Buffer.concat(chunks).toString("latin1") });
            zf.readEntry();
          });
        });
      });
      zf.readEntry();
    });
  });
}

const latin1 = (s: string) => Buffer.from(s, "latin1").toString("latin1");
const utf8AsLatin1 = (s: string) => Buffer.from(s, "utf8").toString("latin1");

describe("shfd.files_zip: the preparation", () => {
  it("a member who is not an admin may; the name is the folder's", async () => {
    const { app } = await setup();
    const bob = await signIn(app, "bob", "bobova-lozinka-1");
    const r = await prepare(app, bob, { dir: "docs", paths: ["docs/plan.md", "docs/slike"] });
    expect(r.ok).toBe(true);
    expect(r.payload.name).toBe("docs.zip");
    expect(r.payload.id).toMatch(/^[A-Za-z0-9_-]{20,}$/);
    // At the root: the shared folder's name.
    const root = await prepare(app, bob, { dir: "", paths: ["a.txt", "docs"] });
    expect(root.payload.name).toBe("projekti.zip");
  });

  it("refuses a path outside the folder, a .. segment, an empty list and more than the limit", async () => {
    const { app } = await setup();
    const ana = await signIn(app, "ana", "anina-lozinka-1");
    for (const payload of [
      { dir: "docs", paths: ["a.txt"] },
      { dir: "docs", paths: ["docs"] },
      { dir: "docs", paths: ["docs/../a.txt"] },
      { dir: "", paths: ["../etc/passwd"] },
      { dir: "", paths: ["/a.txt"] },
      { dir: "", paths: ["docs//plan.md"] },
      { dir: "..", paths: ["../x"] },
      { dir: "", paths: [] },
      { dir: "", paths: Array.from({ length: ZIP_MAX_PATHS + 1 }, (_, i) => `f${i}.txt`) },
    ]) {
      const r = await prepare(app, ana, payload);
      expect(r.ok, JSON.stringify(payload).slice(0, 80)).toBe(false);
      expect(r.payload.code ?? r.payload.details?.reason, JSON.stringify(payload).slice(0, 80)).toBe("validation_error");
    }
  });

  it("without a session it is refused", async () => {
    const { app } = await setup();
    const r = await prepare(app, "", { dir: "", paths: ["a.txt"] });
    expect(r.ok).toBe(false);
    expect(r.payload.details.reason).toBe("unauthenticated");
  });

  it("too many entries in the folders is refused before any download, with a sentence", async () => {
    const { app, fake } = await setup();
    fake.files.velik = Array.from({ length: 20_001 }, (_, i) => file(`velik/f${i}`, 1));
    fake.files[""].push(dir("velik"));
    const ana = await signIn(app, "ana", "anina-lozinka-1");
    const r = await prepare(app, ana, { dir: "", paths: ["velik"] });
    expect(r.ok).toBe(false);
    expect(r.payload.message).toMatch(/20000/);
  });
});

describe("the Files view: Preuzmi for everyone, Obriši for an admin", () => {
  it("Preuzmi comes before Obriši; Obriši holds only for rows the session may change", async () => {
    const { app } = await setup();
    const ana = await signIn(app, "ana", "anina-lozinka-1");
    const bob = await signIn(app, "bob", "bobova-lozinka-1");
    const call = async (cookie: string, action: string, payload: unknown) =>
      (await app.inject({ method: "POST", url: `/gui/udc/${action}`, payload: payload as object, headers: { cookie } })).json() as { payload: any };
    const d = await call(bob, "describe", { collection: "files" });
    const bulk = d.payload.view.bulk_actions as { kind: string; when?: unknown }[];
    expect(bulk.map((b) => b.kind)).toEqual(["download", "remove"]);
    expect(bulk[0].when).toBeUndefined();
    expect(bulk[1].when).toEqual({ field: "can_edit", op: "eq", value: true });
    const rowsOf = async (cookie: string) => (await call(cookie, "list", { collection: "files", filter: { field: "path", op: "eq", value: "docs" } })).payload.rows as any[];
    expect((await rowsOf(ana)).filter((r) => r.name !== "..").every((r) => r.can_edit === true)).toBe(true);
    expect((await rowsOf(bob)).every((r) => r.can_edit === false)).toBe(true);
    // And the server still refuses a member's Obriši.
    const rm = await call(bob, "shfd.files_remove", { items: [{ path: "a.txt", kind: "file", base_hash: "" }] });
    expect(rm.payload.details?.reason ?? rm.payload.code).toBe("forbidden");
  });
});

describe("GET /api/files/zip: the archive", () => {
  it("two files and a folder (recursive, an empty subfolder), our letters and spaces, relative to the folder", async () => {
    const { app, url } = await setup();
    const ana = await signIn(app, "ana", "anina-lozinka-1");
    const r = await prepare(app, ana, { dir: "docs", paths: ["docs/plan.md", "docs/čćšžđ beleška.txt", "docs/slike"] });
    expect(r.ok).toBe(true);
    const res = await download(url, ana, r.payload.id);
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("application/zip");
    expect(res.headers.get("content-disposition")).toBe(`attachment; filename="docs.zip"; filename*=UTF-8''docs.zip`);
    expect(res.headers.get("cache-control")).toBe("no-store");
    expect(res.headers.get("x-content-type-options")).toBe("nosniff");
    expect(res.headers.get("content-length")).toBeNull();
    const entries = await unzip(Buffer.from(await res.arrayBuffer()));
    expect(entries.map((e) => e.name).sort()).toEqual(
      ["plan.md", "čćšžđ beleška.txt", "slike/", "slike/more na plaži.jpg", "slike/prazno/", "slike/duboko/", "slike/duboko/x.bin"].sort(),
    );
    expect(entries.every((e) => e.utf8)).toBe(true);
    const data = (n: string) => entries.find((e) => e.name === n)!.data;
    expect(data("plan.md")).toBe(latin1("# plan\n"));
    expect(data("čćšžđ beleška.txt")).toBe(utf8AsLatin1(CONTENT["docs/čćšžđ beleška.txt"]));
    expect(data("slike/more na plaži.jpg")).toBe(utf8AsLatin1(CONTENT["docs/slike/more na plaži.jpg"]));
    expect(data("slike/duboko/x.bin")).toBe("x".repeat(70_000));
    // mtime from the listing (DOS time is local; the day is enough).
    expect(entries.find((e) => e.name === "plan.md")!.mtime.getUTCDate()).toBe(24);
  });

  it("a member downloads too; at the root the paths are the folder's own", async () => {
    const { app, url } = await setup();
    const bob = await signIn(app, "bob", "bobova-lozinka-1");
    const r = await prepare(app, bob, { dir: "", paths: ["a.txt", "Ćao šđž.txt"] });
    const res = await download(url, bob, r.payload.id);
    expect(res.status).toBe(200);
    expect(res.headers.get("content-disposition")).toContain("projekti.zip");
    const entries = await unzip(Buffer.from(await res.arrayBuffer()));
    expect(entries.map((e) => e.name).sort()).toEqual(["a.txt", "Ćao šđž.txt"].sort());
  });

  it("another session's id, another folder's id, a second use and an expired id are 404", async () => {
    const { app, url, clock } = await setup();
    const ana = await signIn(app, "ana", "anina-lozinka-1");
    const bob = await signIn(app, "bob", "bobova-lozinka-1");
    const paths = { dir: "", paths: ["a.txt"] };

    const a = (await prepare(app, ana, paths)).payload.id;
    expect((await download(url, bob, a)).status).toBe(404);
    // Still ana's: the other session did not use it up.
    const ok = await download(url, ana, a);
    expect(ok.status).toBe(200);
    await ok.arrayBuffer();
    expect((await download(url, ana, a)).status).toBe(404);

    const b = (await prepare(app, ana, paths)).payload.id;
    clock.advance(61_000);
    expect((await download(url, ana, b)).status).toBe(404);

    const c = (await prepare(app, ana, paths)).payload.id;
    await app.inject({ method: "POST", url: "/gui/api/folder", payload: { share_id: OTHER }, headers: { cookie: ana } });
    expect((await download(url, ana, c)).status).toBe(404);

    expect((await download(url, "", c)).status).toBe(401);
    expect((await download(url, ana, "nepostoji")).status).toBe(404);
  });

  it("a file gone between the preparation and the download is skipped and listed; the rest is whole", async () => {
    const { app, url, fake } = await setup();
    const ana = await signIn(app, "ana", "anina-lozinka-1");
    const r = await prepare(app, ana, { dir: "docs", paths: ["docs/plan.md", "docs/slike"] });
    fake.contents.delete("docs/slike/more na plaži.jpg");
    const res = await download(url, ana, r.payload.id);
    expect(res.status).toBe(200);
    const entries = await unzip(Buffer.from(await res.arrayBuffer()));
    const names = entries.map((e) => e.name);
    expect(names).toContain("plan.md");
    expect(names).toContain("slike/duboko/x.bin");
    expect(names).not.toContain("slike/more na plaži.jpg");
    expect(names[names.length - 1]).toBe("_preskoceno.txt");
    const note = Buffer.from(entries.find((e) => e.name === "_preskoceno.txt")!.data, "latin1").toString("utf8");
    expect(note).toContain("docs/slike/more na plaži.jpg");
  });

  it("shfd failing halfway through a file cuts the connection: no archive that looks whole", async () => {
    const { app, url, fake } = await setup();
    const ana = await signIn(app, "ana", "anina-lozinka-1");
    fake.contents.set("docs/slike/duboko/x.bin", () => {
      let sent = false;
      return new ReadableStream<Uint8Array>({
        pull(c) {
          if (!sent) {
            sent = true;
            c.enqueue(enc("x".repeat(1000)));
          } else c.error(new Error("shfd went away"));
        },
      });
    });
    const r = await prepare(app, ana, { dir: "docs", paths: ["docs/plan.md", "docs/slike"] });
    const res = await download(url, ana, r.payload.id);
    expect(res.status).toBe(200);
    await expect(res.arrayBuffer()).rejects.toThrow();
  });

  it("shfd unreachable before a file cuts the connection too", async () => {
    const { app, url, fake } = await setup();
    const ana = await signIn(app, "ana", "anina-lozinka-1");
    const r = await prepare(app, ana, { dir: "", paths: ["a.txt", "Ćao šđž.txt"] });
    const real = fake.fetch;
    fake.fetch = async (input, init) => {
      if (String(input).includes("files/content") && String(input).includes(encodeURIComponent("Ćao"))) throw new TypeError("fetch failed");
      return real(input, init);
    };
    const res = await download(url, ana, r.payload.id);
    await expect(res.arrayBuffer()).rejects.toThrow();
  });

  it("a 401 from shfd ends the session and the download", async () => {
    const { app, url, fake } = await setup();
    const ana = await signIn(app, "ana", "anina-lozinka-1");
    const r = await prepare(app, ana, { dir: "", paths: ["a.txt"] });
    fake.devices.forEach((d) => (d.token = "opozvan"));
    // Cut before or after the headers: either way no archive.
    await expect(download(url, ana, r.payload.id).then((res) => res.arrayBuffer())).rejects.toThrow();
    expect(gui!.sessions.size()).toBe(0);
  });

  it("the browser leaving stops the reading from shfd", async () => {
    const { app, url, fake } = await setup();
    const ana = await signIn(app, "ana", "anina-lozinka-1");
    let pulls = 0;
    let cancelled = false;
    fake.contents.set("a.txt", () => new ReadableStream<Uint8Array>({
      pull(c) {
        pulls++;
        c.enqueue(new Uint8Array(64 * 1024));
      },
      cancel() {
        cancelled = true;
      },
    }));
    const r = await prepare(app, ana, { dir: "", paths: ["a.txt"] });
    const ctl = new AbortController();
    const res = await fetch(`${url}/gui/api/files/zip?id=${r.payload.id}`, { headers: { cookie: ana }, signal: ctl.signal });
    const reader = res.body!.getReader();
    await reader.read();
    ctl.abort();
    await new Promise((ok) => setTimeout(ok, 200));
    const at = pulls;
    await new Promise((ok) => setTimeout(ok, 200));
    expect(cancelled || pulls === at).toBe(true);
    expect(pulls).toBeLessThan(1000);
  });
});

describe("zip.ts", () => {
  it("planZip walks the selected folders, keeps each path once and counts against the limit", async () => {
    const listing: Record<string, FileEntry[]> = {
      "": [dir("d"), file("f", 1)],
      d: [file("d/a", 1), dir("d/e")],
      "d/e": [],
    };
    const list = async (d: string) => listing[d];
    const plan = await planZip(list, "", ["d", "f", "d", "nema"], () => null);
    expect(plan.entries.map((e) => `${e.kind}:${e.arc}`)).toEqual(["dir:d", "file:d/a", "dir:d/e", "file:f"]);
    expect(plan.skipped).toEqual([{ path: "nema", reason: "not_found" }]);
    await expect(planZip(list, "", ["d"], () => null, 2)).rejects.toThrow();
  });

  it("ZipJobs: bound to the session and the folder, once, a few per session", () => {
    let t = 0;
    const jobs = new ZipJobs(() => t, 1000, 2);
    const job = (s: string) => jobs.put({ sessionId: s, shareId: "f", name: "x.zip", lang: "sr", entries: [], skipped: [] });
    const a = job("s1");
    expect(jobs.take(a.id, "s2", "f")).toBeUndefined();
    expect(jobs.take(a.id, "s1", "g")).toBeUndefined();
    expect(jobs.take(a.id, "s1", "f")).toBeDefined();
    expect(jobs.take(a.id, "s1", "f")).toBeUndefined();
    const b = job("s1");
    job("s1");
    job("s1");
    expect(jobs.take(b.id, "s1", "f")).toBeUndefined(); // pushed out by the newer ones
    const c = job("s2");
    t = 1000;
    expect(jobs.take(c.id, "s2", "f")).toBeUndefined();
  });

  it("names and the Content-Disposition", () => {
    expect(zipName("docs/slike", "projekti")).toBe("slike.zip");
    expect(zipName("", "Moj \"folder\"")).toBe("Moj _folder_.zip");
    expect(attachmentDisposition("Ćao šđž.zip")).toBe(`attachment; filename="_ao ___.zip"; filename*=UTF-8''%C4%86ao%20%C5%A1%C4%91%C5%BE.zip`);
    expect(zipRequestProblem("docs", ["docs/a"], "sr")).toBeNull();
    expect(zipRequestProblem("docs", ["docsx/a"], "en")).toMatch(/not in the folder/);
  });
});
