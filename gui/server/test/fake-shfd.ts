// A fake shfd for tests: just enough of the HTTP API, behind a fetch.

import type { FileEntry, LogLine, MemberInfo } from "../src/shfd.js";

export interface FakeMember extends MemberInfo {
  token: string;
}

export class FakeShfd {
  share = "3f0c0000-0000-4000-8000-000000000001";
  members: FakeMember[] = [];
  log: LogLine[] = [];
  calls: string[] = [];
  /** The listing of each folder ("" is the root). */
  files: Record<string, FileEntry[]> = {
    "": [
      { path: "docs", name: "docs", kind: "dir", size: 0, mtime: null, hash: "", seq: 1, updated_by: "ana", thumb: "" },
      { path: "a.txt", name: "a.txt", kind: "file", size: 3, mtime: "2026-11-24T10:00:00Z", hash: "aa".repeat(32), seq: 2, updated_by: "bob", thumb: "" },
    ],
    docs: [
      { path: "docs/plan.md", name: "plan.md", kind: "file", size: 7, mtime: "2026-11-24T10:00:00Z", hash: "bb".repeat(32), seq: 3, updated_by: "ana", thumb: "" },
    ],
  };
  /** Thumbnails by content hash; every size gets the same bytes. */
  thumbs = new Map<string, Uint8Array>();
  /** File contents by path; a function makes the body (a stream, say). */
  contents = new Map<string, Uint8Array | (() => ReadableStream<Uint8Array>)>();
  /** The headers of the last content request. */
  lastContentHeaders: Headers | null = null;
  /** The If-None-Match of the last thumbnail request. */
  lastIfNoneMatch: string | null = null;
  private n = 0;

  constructor() {
    this.add("ana", "admin");
    this.add("bob", "member");
  }

  add(username: string, role: "admin" | "member"): FakeMember {
    const m: FakeMember = {
      username,
      role,
      status: "pending",
      created_at: "2026-11-24T10:00:00Z",
      revoked_at: null,
      last_seen_at: null,
      last_push_at: null,
      last_seq_acked: 0,
      behind: 0,
      ssh_fingerprint: "",
      connected: 0,
      token: `shf_${username}_${++this.n}`,
    };
    this.members.push(m);
    return m;
  }

  /** Appends a line to the fake service.log; offsets grow like bytes. */
  append(l: Omit<LogLine, "offset" | "raw">): void {
    const raw = `${l.ts} ${l.type} ${l.member} ${l.op} ${l.path}`;
    const offset = this.size();
    this.log.push({ ...l, offset, raw } as LogLine);
  }

  size(): number {
    return this.log.reduce((n, l) => n + l.raw.length + 1, 0);
  }

  /** Where an entry of path lives: its folder's listing and its index. */
  private find(path: string): [FileEntry[], number] {
    const i = path.lastIndexOf("/");
    const list = this.files[i < 0 ? "" : path.slice(0, i)] ?? [];
    return [list, list.findIndex((f) => f.path === path)];
  }

  /** Enough of POST /ops: move, delete and rmdir under shfd's rules. */
  private op(init: RequestInit | undefined, json: (s: number, b: unknown) => Response, err: (s: number, c: string) => Response): Response {
    const req = JSON.parse(String(init?.body ?? "{}")) as { op: string; path: string; new_path?: string; base_hash?: string };
    this.ops.push(req);
    const [list, i] = this.find(req.path);
    const cur = list[i];
    if (req.op === "rmdir") {
      if (!cur) return json(200, { status: "unchanged", path: req.path, events: [], head: 3 });
      if (cur.kind !== "dir") return err(409, "kind_mismatch");
      if ((this.files[req.path] ?? []).length > 0) return err(409, "dir_not_empty");
      list.splice(i, 1);
      return json(200, { status: "applied", path: req.path, events: [], head: 4 });
    }
    if (!cur) return req.op === "delete" ? json(200, { status: "unchanged", path: req.path, events: [], head: 3 }) : err(404, "not_found");
    if (cur.kind === "dir") return err(409, "is_dir");
    if (cur.hash !== req.base_hash) return err(409, "stale_base");
    if (req.op === "delete") {
      list.splice(i, 1);
      return json(200, { status: "applied", path: req.path, events: [], head: 4 });
    }
    const to = req.new_path ?? "";
    const [dst, j] = this.find(to);
    if (j >= 0) return err(409, "dest_exists");
    list.splice(i, 1);
    dst.push({ ...cur, path: to, name: to.split("/").pop()! });
    return json(200, { status: "applied", path: to, events: [], head: 4 });
  }

  /** Every op the GUI sent, as sent. */
  ops: { op: string; path: string; new_path?: string; base_hash?: string }[] = [];

  fetch: typeof fetch = async (input, init) => {
    const url = new URL(String(input));
    const auth = new Headers(init?.headers).get("authorization") ?? "";
    const token = auth.replace(/^Bearer /, "");
    const method = init?.method ?? "GET";
    this.calls.push(`${method} ${url.pathname}${url.search}`);
    const json = (status: number, body: unknown) =>
      new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
    const err = (status: number, code: string) => json(status, { error: { code, message: code } });
    if (url.pathname === "/healthz") return json(200, { service: "shfd", version: "abc1234", ok: true });
    // As shfd: the hash and the size are checked before the token.
    const tm = new RegExp(`^/v1/shares/[^/]+/admin/thumbs/([^/]+)$`).exec(url.pathname);
    if (tm) {
      const size = url.searchParams.get("size") ?? "m";
      if (!/^[0-9a-f]{64}$/.test(tm[1]) || !["s", "m", "l"].includes(size)) return err(400, "invalid_request");
    }
    const me = this.members.find((m) => m.token === token && m.status !== "revoked");
    if (!me) return err(401, "unauthorized");
    if (url.pathname === "/v1/whoami") {
      return json(200, { share_id: this.share, share_name: "projekti", username: me.username, role: me.role });
    }
    if (url.pathname === `/v1/shares/${this.share}/ops` && method === "POST") return this.op(init, json, err);
    const prefix = `/v1/shares/${this.share}/admin/`;
    if (!url.pathname.startsWith(prefix)) return err(401, "unauthorized");
    if (me.role !== "admin") return err(403, "forbidden");
    const rest = url.pathname.slice(prefix.length);
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    if (rest === "members" && method === "GET") {
      return json(200, { head: 0, members: this.members.map(({ token: _t, ...m }) => m) });
    }
    if (rest === "members" && method === "POST") {
      if (this.members.some((m) => m.username === body.username && m.status !== "revoked")) return err(409, "member_exists");
      const m = this.add(body.username, body.admin ? "admin" : "member");
      return json(200, { member: m, token: m.token });
    }
    const mm = /^members\/([^/]+)\/(revoke|role|reset-token)$/.exec(rest);
    if (mm) {
      const t = this.members.find((m) => m.username === decodeURIComponent(mm[1]));
      if (!t) return err(404, "unknown_member");
      const admins = this.members.filter((m) => m.role === "admin" && m.status !== "revoked");
      if (mm[2] === "revoke") {
        if (t.role === "admin" && admins.length === 1) return err(409, "last_admin");
        t.status = "revoked";
        return json(200, { member: t });
      }
      if (mm[2] === "role") {
        if (t.role === "admin" && body.role !== "admin" && admins.length === 1) return err(409, "last_admin");
        const old = t.role;
        t.role = body.role;
        return json(200, { member: t, old_role: old });
      }
      t.token = `shf_${t.username}_${++this.n}`;
      return json(200, { member: t, token: t.token });
    }
    if (rest === "log") {
      const limit = Number(url.searchParams.get("limit") ?? 200);
      const after = url.searchParams.get("after");
      let lines = after === null ? this.log.slice(-limit) : this.log.filter((l) => l.offset >= Number(after)).slice(0, limit);
      const next = lines.length ? lines[lines.length - 1].offset + lines[lines.length - 1].raw.length + 1 : after === null ? this.size() : Number(after);
      return json(200, { offset: lines[0]?.offset ?? next, next_offset: next, size: this.size(), lines });
    }
    if (rest === "stats") {
      return json(200, { share_id: this.share, head: 3, files: 2, files_size: 10, dirs: 1, trash: 0, trash_size: 0, purged: 0, open_uploads: 0 });
    }
    if (rest === "files") {
      const dir = url.searchParams.get("dir") ?? "";
      const entries = this.files[dir];
      if (!entries) return err(404, "not_found");
      return json(200, { dir, entries });
    }
    if (tm) {
      const size = url.searchParams.get("size") ?? "m";
      const bytes = this.thumbs.get(tm[1]);
      if (!bytes) return err(404, "not_found");
      const etag = `"${tm[1]}.${size}"`;
      const headers = { etag, "cache-control": "private, max-age=31536000, immutable" };
      this.lastIfNoneMatch = new Headers(init?.headers).get("if-none-match");
      if (this.lastIfNoneMatch === etag) return new Response(null, { status: 304, headers });
      return new Response(bytes, { status: 200, headers: { ...headers, "content-type": "image/jpeg" } });
    }
    if (rest === "files/content") {
      // Enough of shfd: the whitelist, download=1, the ETag, one range.
      this.lastContentHeaders = new Headers(init?.headers);
      const path = url.searchParams.get("path") ?? "";
      if (path.includes("..")) return err(400, "invalid_path");
      if (path === "changed.txt") return err(409, "file_changed");
      const c = this.contents.get(path);
      if (c === undefined) return err(404, "not_found");
      const name = path.split("/").pop()!;
      const ext = name.split(".").pop()!.toLowerCase();
      const inline = ({ txt: "text/plain; charset=utf-8", png: "image/png", jpg: "image/jpeg" } as Record<string, string>)[ext];
      const attach = !inline || url.searchParams.get("download") === "1";
      const headers: Record<string, string> = {
        "content-type": inline ?? "application/octet-stream",
        "content-disposition": `${attach ? "attachment" : "inline"}; filename="${name}"`,
        etag: `"${"cc".repeat(32)}"`,
        "cache-control": "private, no-cache",
        "content-security-policy": "sandbox",
        "x-content-type-options": "nosniff",
        "accept-ranges": "bytes",
        "x-internal": "not for the browser",
      };
      if (this.lastContentHeaders.get("if-none-match") === headers.etag) return new Response(null, { status: 304, headers });
      if (typeof c === "function") return new Response(c(), { status: 200, headers });
      const range = /^bytes=(\d+)-(\d+)$/.exec(this.lastContentHeaders.get("range") ?? "");
      if (range) {
        const [a, b] = [Number(range[1]), Math.min(Number(range[2]), c.length - 1)];
        const part = c.slice(a, b + 1);
        return new Response(part, {
          status: 206,
          headers: { ...headers, "content-range": `bytes ${a}-${b}/${c.length}`, "content-length": String(part.length) },
        });
      }
      return new Response(c, { status: 200, headers: { ...headers, "content-length": String(c.length) } });
    }
    if (rest === "trash") return json(200, { entries: [], count: 0, total_size: 0 });
    return err(404, "not_found");
  };
}
