// A fake shfd for tests: just enough of the HTTP API, behind a fetch.

import type { Device, FileEntry, LogLine, MemberDevice, MemberInfo } from "../src/shfd.js";

export interface FakeMember extends MemberInfo {
  token: string;
}

/** An account of the fake server. */
export interface FakeUser {
  id: number;
  username: string;
  display_name: string;
  password: string;
  must_change: boolean;
  disabled: boolean;
}

/** A signed-in device of an account. */
export interface FakeDevice extends Device {
  user: string;
  token: string;
}

/** A second folder: the account is a member there with role. */
export interface FakeFolder {
  share_id: string;
  name: string;
  members: Record<string, "admin" | "member">;
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
  /** Accounts, their devices (sign-ins) and other folders. */
  users: FakeUser[] = [];
  devices: FakeDevice[] = [];
  folders: FakeFolder[] = [];
  /** Failed sign-ins per username; at failLimit the answer is 429. */
  failures = new Map<string, number>();
  failLimit = 5;
  /** The device_name of the last sign-in. */
  lastDeviceName = "";
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

  /** An account; mustChange makes its password temporary. */
  user(username: string, password: string, opts: { mustChange?: boolean; displayName?: string } = {}): FakeUser {
    const u: FakeUser = {
      id: this.users.length + 1,
      username,
      display_name: opts.displayName ?? "",
      password,
      must_change: !!opts.mustChange,
      disabled: false,
    };
    this.users.push(u);
    return u;
  }

  /** A member of the main folder linked to an account (admin/members {user}). */
  addAccountMember(username: string, role: "admin" | "member"): FakeMember {
    const m = this.add(username, role);
    m.token = "";
    m.account = username;
    m.user_id = this.users.find((u) => u.username === username)?.id;
    m.devices = [];
    return m;
  }

  /** A client device of an account that has a folder credential in the main folder. */
  clientDevice(username: string, name: string): FakeDevice {
    const d: FakeDevice = {
      id: 100 + this.devices.length + 1,
      name,
      kind: "client",
      created_at: "2026-11-24T09:00:00Z",
      last_seen_at: "2026-11-24T11:00:00Z",
      user: username,
      token: `shd_${username}_${name}`,
    };
    this.devices.push(d);
    const m = this.members.find((x) => x.account === username);
    m?.devices?.push(this.memberDevice(d));
    return d;
  }

  private memberDevice(d: FakeDevice): MemberDevice {
    return {
      id: d.id,
      name: d.name,
      kind: d.kind,
      ssh_fingerprint: d.kind === "client" ? "SHA256:abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG" : "",
      created_at: d.created_at,
      last_seen_at: d.last_seen_at ?? null,
      revoked: !!d.revoked_at,
      connected: 0,
    };
  }

  /** The folders of an account, as /v1/me lists them. */
  foldersOf(username: string) {
    const out: { share_id: string; name: string; role: string; member: string; last_change_at: string }[] = [];
    const m = this.members.find((x) => x.account === username && x.status !== "revoked");
    if (m) out.push({ share_id: this.share, name: "projekti", role: m.role, member: m.username, last_change_at: "2026-11-24T10:00:00Z" });
    for (const f of this.folders) {
      const role = f.members[username];
      if (role) out.push({ share_id: f.share_id, name: f.name, role, member: username, last_change_at: "2026-11-20T08:00:00Z" });
    }
    return out;
  }

  /** The live device of a device token. */
  private deviceOf(token: string): FakeDevice | undefined {
    const d = this.devices.find((x) => x.token === token && !x.revoked_at);
    if (!d) return undefined;
    const u = this.users.find((x) => x.username === d.user);
    return u && !u.disabled ? d : undefined;
  }

  /** The routes of accounts; undefined when the path is not one of them. */
  private accounts(url: URL, method: string, token: string, init: RequestInit | undefined, json: (s: number, b: unknown) => Response, err: (s: number, c: string) => Response): Response | undefined {
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    if (url.pathname === "/v1/auth/login" && method === "POST") {
      const fails = this.failures.get(body.username) ?? 0;
      if (fails >= this.failLimit) {
        return new Response(JSON.stringify({ error: { code: "too_many_attempts", message: "too many failed attempts; try again in 4s" } }), {
          status: 429,
          headers: { "content-type": "application/json", "retry-after": "4" },
        });
      }
      const u = this.users.find((x) => x.username === body.username);
      if (!u || u.disabled || u.password !== body.password) {
        this.failures.set(body.username, fails + 1);
        return err(401, "invalid_credentials");
      }
      this.lastDeviceName = body.device_name;
      const d: FakeDevice = {
        id: 100 + this.devices.length + 1,
        name: body.device_name,
        kind: body.kind,
        created_at: "2026-11-24T12:00:00Z",
        last_seen_at: "2026-11-24T12:00:00Z",
        expires_at: "2026-11-25T00:00:00Z",
        user: u.username,
        token: `shd_${u.username}_${this.devices.length + 1}`,
      };
      this.devices.push(d);
      const { password: _p, disabled: _d, ...user } = u;
      return json(200, { token: d.token, user, must_change: u.must_change });
    }
    const isAccount = ["/v1/auth/logout", "/v1/me", "/v1/me/password", "/v1/users"].includes(url.pathname) || url.pathname.startsWith("/v1/me/devices/");
    if (!isAccount) return undefined;
    const d = this.deviceOf(token);
    if (!d) return err(401, "unauthorized");
    const u = this.users.find((x) => x.username === d.user)!;
    if (url.pathname === "/v1/auth/logout") {
      d.revoked_at = "2026-11-24T13:00:00Z";
      return json(200, { status: "ok" });
    }
    if (url.pathname === "/v1/me") {
      const { password: _p, disabled: _d, ...user } = u;
      return json(200, {
        user,
        folders: this.foldersOf(u.username),
        devices: this.devices.filter((x) => x.user === u.username && !x.revoked_at).map(({ user: _u, token: t, ...dev }) => ({ ...dev, current: t === token })),
        password_rules: { min_len: 12, max_len: 256, not_username: true, differ_from_old: true },
      });
    }
    if (url.pathname === "/v1/me/password") {
      if (body.old !== u.password) return err(403, "wrong_password");
      if (String(body.new).length < 12) return json(400, { error: { code: "invalid_password", message: "the password must have at least 12 characters" } });
      u.password = body.new;
      u.must_change = false;
      return json(200, { status: "ok" });
    }
    if (u.must_change) return err(403, "password_change_required");
    const dm = /^\/v1\/me\/devices\/(\d+)$/.exec(url.pathname);
    if (dm && method === "DELETE") {
      const t = this.devices.find((x) => x.id === Number(dm[1]) && x.user === u.username && !x.revoked_at);
      if (!t) return err(404, "unknown_device");
      t.revoked_at = "2026-11-24T13:00:00Z";
      return json(200, { status: "ok" });
    }
    if (url.pathname === "/v1/users") {
      if (!this.foldersOf(u.username).some((f) => f.role === "admin")) return err(403, "forbidden");
      const q = (url.searchParams.get("q") ?? "").toLowerCase();
      return json(200, this.users.filter((x) => x.username.startsWith(q)).map((x) => ({ username: x.username, display_name: x.display_name })));
    }
    return err(404, "not_found");
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
    let url = new URL(String(input));
    const auth = new Headers(init?.headers).get("authorization") ?? "";
    const token = auth.replace(/^Bearer /, "");
    const method = init?.method ?? "GET";
    this.calls.push(`${method} ${url.pathname}${url.search}`);
    const json = (status: number, body: unknown) =>
      new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });
    const err = (status: number, code: string) => json(status, { error: { code, message: code } });
    if (url.pathname === "/healthz") return json(200, { service: "shfd", version: "abc1234", ok: true });
    // As shfd: the hash and the size are checked before the token.
    const tm = new RegExp(`^/v1/shares/[^/]+/(?:admin/)?thumbs/([^/]+)$`).exec(url.pathname);
    if (tm) {
      const size = url.searchParams.get("size") ?? "m";
      if (!/^[0-9a-f]{64}$/.test(tm[1]) || !["s", "m", "l"].includes(size)) return err(400, "invalid_request");
    }
    const acct = this.accounts(url, method, token, init, json, err);
    if (acct) return acct;
    // A member token, or an account's device token acting as its member.
    let me = token ? this.members.find((m) => m.token === token && m.status !== "revoked") : undefined;
    const dev = this.deviceOf(token);
    if (!me && dev) {
      const u = this.users.find((x) => x.username === dev.user)!;
      if (u.must_change) return err(403, "password_change_required");
      const other = this.folders.find((f) => url.pathname.startsWith(`/v1/shares/${f.share_id}/`));
      if (other) {
        const role = other.members[u.username];
        if (!role) return err(401, "unauthorized");
        // Another folder: the same listing as the main one, as this member.
        me = { ...this.members[0], username: u.username, role, token };
        url = new URL(url.toString().replace(other.share_id, this.share));
      } else {
        me = this.members.find((m) => m.account === dev.user && m.status !== "revoked");
      }
    }
    if (!me) return err(401, "unauthorized");
    if (url.pathname === "/v1/whoami") {
      return json(200, { share_id: this.share, share_name: "projekti", username: me.username, role: me.role });
    }
    if (url.pathname === `/v1/shares/${this.share}/ops` && method === "POST") return this.op(init, json, err);
    const prefix = `/v1/shares/${this.share}/admin/`;
    const memberPrefix = `/v1/shares/${this.share}/`;
    let rest: string;
    if (url.pathname.startsWith(prefix)) {
      if (me.role !== "admin") return err(403, "forbidden");
      rest = url.pathname.slice(prefix.length);
      // The member routes are the only way to files, content and thumbnails now.
      if (rest === "files" || rest === "files/content" || rest.startsWith("thumbs/")) this.calls.push("ADMIN-FILE-ROUTE");
    } else if (url.pathname.startsWith(memberPrefix)) {
      rest = url.pathname.slice(memberPrefix.length);
      if (rest !== "files" && rest !== "files/content" && !rest.startsWith("thumbs/")) return err(404, "not_found");
    } else return err(401, "unauthorized");
    const body = init?.body ? JSON.parse(String(init.body)) : {};
    if (rest === "members" && method === "GET") {
      return json(200, { head: 0, members: this.members.map(({ token: _t, ...m }) => m) });
    }
    if (rest === "members" && method === "POST" && body.user) {
      const u = this.users.find((x) => x.username === body.user);
      if (!u) return err(404, "unknown_user");
      const old = this.members.find((m) => m.account === body.user);
      if (old && old.status !== "revoked") return err(409, "member_exists");
      if (old) {
        old.status = "active";
        return json(200, { member: old, reactivated: true });
      }
      const m = this.addAccountMember(body.user, body.admin ? "admin" : "member");
      return json(200, { member: m });
    }
    const dv = /^members\/([^/]+)\/devices\/(\d+)$/.exec(rest);
    if (dv && method === "DELETE") {
      const t = this.members.find((m) => m.username === decodeURIComponent(dv[1]));
      const d = t?.devices?.find((x) => x.id === Number(dv[2]) && !x.revoked);
      if (!t || !d) return err(404, "unknown_device");
      d.revoked = true;
      d.revoked_at = "2026-11-24T13:00:00Z";
      return json(200, { member: t });
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
