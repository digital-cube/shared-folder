import { describe, expect, it } from "vitest";
import { kindOf, LogFeed, short, summarize, toRow } from "../src/logs.js";
import { SessionStore } from "../src/sessions.js";
import { ShfdClient, type LogLine } from "../src/shfd.js";
import { FakeShfd } from "./fake-shfd.js";

const line = (l: Partial<LogLine>): LogLine => ({
  offset: 0,
  ts: "2026-11-24T10:15:42Z",
  type: "event",
  member: "ana",
  op: "put",
  path: "a.txt",
  raw: "raw",
  ...l,
});

describe("log rows", () => {
  it("maps the line types to the Tip column", () => {
    expect(kindOf(line({}))).toBe("izmena");
    expect(kindOf(line({ type: "transfer", outcome: "ok", dir: "upload" }))).toBe("transfer");
    expect(kindOf(line({ type: "transfer", outcome: "denied" }))).toBe("odbijeno");
    expect(kindOf(line({ type: "transfer", outcome: "failed" }))).toBe("odbijeno");
    expect(kindOf(line({ type: "key-replaced" }))).toBe("kljuc");
    expect(kindOf(line({ type: "sync", outcome: "ok" }))).toBe("sync");
    expect(kindOf(line({ type: "sync", outcome: "failed" }))).toBe("odbijeno");
    expect(kindOf(line({ type: "other" }))).toBe("ostalo");
  });

  it("an event row", () => {
    const r = toRow(line({ offset: 120, seq: 4813, op: "move", path: "novo.md", old_path: "staro.md" }));
    expect(r).toMatchObject({ id: "120", kind: "izmena", op: "move", path: "staro.md → novo.md", seq: 4813, error: false });
  });

  it("a refused transfer is an error and shows the outcome as the operation", () => {
    const r = toRow(line({ type: "transfer", op: "-", dir: "-", outcome: "denied", detail: "revoked", member: "#7", path: "storage/x" }));
    expect(r).toMatchObject({ kind: "odbijeno", op: "denied", error: true, detail: "revoked", seq: null, member: "#7" });
    const ok = toRow(line({ type: "transfer", op: "upload", dir: "upload", outcome: "ok", path: "tmp/u_1" }));
    expect(ok).toMatchObject({ kind: "transfer", op: "upload", error: false });
  });

  it("a key-replaced row shows both fingerprints, cut", () => {
    const r = toRow(line({ type: "key-replaced", op: "key-replaced", path: "", detail: "old=SHA256:Yp7cAAAAAAAA02kd new=SHA256:9aQeBBBBBBBBzz11" }));
    expect(r.path).toBe("SHA256:Yp7c…02kd → SHA256:9aQe…zz11");
    expect(r.kind).toBe("kljuc");
  });

  it("a sync row shows its numbers; a failed one is an error", () => {
    const r = toRow(line({ type: "sync", op: "sync", outcome: "ok", path: "", detail: "from=0 to=3 sent=3" }));
    expect(r).toMatchObject({ kind: "sync", op: "sync", path: "from=0 to=3 sent=3", error: false });
    expect(toRow(line({ type: "sync", op: "sync", outcome: "failed", detail: "error=x" })).error).toBe(true);
  });

  it("an unparsed line keeps its raw text", () => {
    const r = toRow(line({ type: "other", op: "", path: "", member: "", ts: "", raw: "čudan red" }));
    expect(r).toMatchObject({ kind: "ostalo", raw: "čudan red", op: "" });
  });

  it("short keeps short fingerprints whole", () => {
    expect(short("SHA256:abc")).toBe("SHA256:abc");
    expect(short("")).toBe("");
  });
});

describe("summarize", () => {
  it("counts today, transfers, refusals and activity per member in the last hour", () => {
    const now = new Date("2026-11-24T12:00:00");
    const at = (h: number, m = 0) => new Date(2026, 10, 24, h, m).toISOString();
    const s = summarize(
      [
        line({ ts: new Date(2026, 10, 23, 23, 0).toISOString() }), // yesterday
        line({ ts: at(9) }),
        line({ ts: at(11, 30), member: "marko" }),
        line({ ts: at(11, 31), member: "marko", type: "transfer", outcome: "ok", dir: "upload" }),
        line({ ts: at(11, 32), member: "ana", type: "transfer", outcome: "ok", dir: "download" }),
        line({ ts: at(11, 33), member: "#7", type: "transfer", outcome: "denied" }),
      ],
      now,
    );
    expect(s).toMatchObject({ today: 5, last_hour: 4, transfers: 2, uploads: 1, downloads: 1, refused: 1 });
    expect(s.per_member).toEqual([
      { member: "marko", count: 2 },
      { member: "ana", count: 1 },
      { member: "#7", count: 1 },
    ]);
  });
});

describe("LogFeed", () => {
  it("loads, then polls with after=next_offset and hands over only new lines", async () => {
    const fake = new FakeShfd();
    fake.append({ ts: "2026-11-24T10:00:00Z", type: "event", seq: 1, member: "ana", op: "put", path: "a.txt" });
    const sessions = new SessionStore();
    sessions.create({ token: fake.members[0].token, shareId: fake.share, shareName: "p", username: "ana", role: "admin" });
    const got: LogLine[][] = [];
    const feed = new LogFeed({ client: new ShfdClient("http://shfd", fake.fetch), sessions, onLines: (_s, l) => void got.push(l) });

    await feed.tick(); // first load
    expect((await feed.lines(fake.share, "x")).map((l) => l.path)).toEqual(["a.txt"]);
    expect(got).toEqual([]);

    await feed.tick(); // nothing new
    expect(got).toEqual([]);
    fake.append({ ts: "2026-11-24T10:00:01Z", type: "event", seq: 2, member: "bob", op: "mkdir", path: "docs" });
    await feed.tick();
    expect(got.map((g) => g.map((l) => l.path))).toEqual([["docs"]]);
    expect(fake.calls.filter((c) => c.includes("/admin/log")).at(-1)).toContain(`after=${fake.log[1].offset}`);
    expect((await feed.lines(fake.share, "x")).length).toBe(2);
  });

  it("drops the sessions of a token shfd refuses, and stops polling the folder", async () => {
    const fake = new FakeShfd();
    const sessions = new SessionStore();
    const s = sessions.create({ token: fake.members[0].token, shareId: fake.share, shareName: "p", username: "ana", role: "admin" });
    const feed = new LogFeed({ client: new ShfdClient("http://shfd", fake.fetch), sessions, onLines: () => {} });
    fake.members[0].status = "revoked";
    await feed.tick();
    expect(sessions.get(s.id)).toBeUndefined();
    const before = fake.calls.length;
    await feed.tick();
    expect(fake.calls.length).toBe(before);
  });
});
