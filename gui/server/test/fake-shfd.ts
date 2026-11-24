// A fake shfd for tests: just enough of the HTTP API, behind a fetch.

import type { LogLine, MemberInfo } from "../src/shfd.js";

export interface FakeMember extends MemberInfo {
  token: string;
}

export class FakeShfd {
  share = "3f0c0000-0000-4000-8000-000000000001";
  members: FakeMember[] = [];
  log: LogLine[] = [];
  calls: string[] = [];
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
    const me = this.members.find((m) => m.token === token && m.status !== "revoked");
    if (!me) return err(401, "unauthorized");
    if (url.pathname === "/v1/whoami") {
      return json(200, { share_id: this.share, share_name: "projekti", username: me.username, role: me.role });
    }
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
      if (dir === "") {
        return json(200, {
          dir: "",
          entries: [
            { path: "docs", name: "docs", kind: "dir", size: 0, mtime: null, hash: "", seq: 1, updated_by: "ana" },
            { path: "a.txt", name: "a.txt", kind: "file", size: 3, mtime: "2026-11-24T10:00:00Z", hash: "aa".repeat(32), seq: 2, updated_by: "bob" },
          ],
        });
      }
      if (dir === "docs") {
        return json(200, {
          dir: "docs",
          entries: [{ path: "docs/plan.md", name: "plan.md", kind: "file", size: 7, mtime: "2026-11-24T10:00:00Z", hash: "bb".repeat(32), seq: 3, updated_by: "ana" }],
        });
      }
      return err(404, "not_found");
    }
    if (rest === "trash") return json(200, { entries: [], count: 0, total_size: 0 });
    return err(404, "not_found");
  };
}
