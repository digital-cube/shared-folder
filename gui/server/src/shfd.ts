// The shfd HTTP API, always called with the token of the signed-in member,
// so shfd itself decides what that member may do (401 / 403).

export interface Whoami {
  share_id: string;
  share_name: string;
  username: string;
  role: string;
}

export interface MemberInfo {
  username: string;
  role: "admin" | "member";
  status: "active" | "pending" | "revoked";
  created_at: string;
  revoked_at: string | null;
  last_seen_at: string | null;
  last_push_at: string | null;
  last_seq_acked: number;
  behind: number;
  ssh_fingerprint: string;
  connected: number;
}

export interface MemberList {
  head: number;
  members: MemberInfo[];
}

export interface TokenResponse {
  member: MemberInfo;
  token: string;
  reactivated?: boolean;
}

export interface LogLine {
  offset: number;
  ts: string;
  type: "event" | "transfer" | "key-replaced" | "sync" | "other";
  seq?: number;
  member: string;
  op: string;
  path: string;
  old_path?: string;
  dir?: string;
  outcome?: string;
  detail?: string;
  raw: string;
}

export interface LogPage {
  offset: number;
  next_offset: number;
  size: number;
  lines: LogLine[];
}

export interface FileEntry {
  path: string;
  name: string;
  kind: "file" | "dir";
  size: number;
  mtime: string | null;
  hash: string;
  seq: number;
  updated_by: string;
  /** The thumbnail: "ready", "pending", "failed", or "" (none for this kind). */
  thumb?: ThumbState;
}

export type ThumbState = "ready" | "pending" | "failed" | "";
export type ThumbSize = "s" | "m" | "l";

/** shfd's answer for a thumbnail, as it came: the GUI passes it through. */
export interface ThumbResponse {
  status: number;
  headers: Headers;
  body: Uint8Array;
}

export interface DirListing {
  dir: string;
  entries: FileEntry[];
}

export interface TrashEntry {
  id: number;
  orig_path: string;
  trash_name?: string;
  hash: string;
  size: number;
  reason: string;
  by: string;
  at: string;
  event_seq?: number;
}

export interface TrashList {
  entries: TrashEntry[];
  count: number;
  total_size: number;
}

export interface RestoreResult {
  trash_id: number;
  status: string;
  path: string;
  renamed?: boolean;
  head?: number;
}

export interface Stats {
  share_id: string;
  head: number;
  files: number;
  files_size: number;
  dirs: number;
  trash: number;
  trash_size: number;
  purged: number;
  open_uploads: number;
}

export interface Health {
  service: string;
  version: string;
  ok: boolean;
  error?: string;
}

/** A refusal of shfd: its HTTP status and error code. */
export class ShfdError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = "ShfdError";
  }
}

export class ShfdClient {
  constructor(
    readonly baseUrl: string,
    private readonly fetchFn: typeof fetch = fetch,
    private readonly timeoutMs = 10_000,
  ) {}

  private async call<T>(method: string, path: string, token: string | undefined, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { accept: "application/json" };
    if (token) headers.authorization = `Bearer ${token}`;
    if (body !== undefined) headers["content-type"] = "application/json";
    let res: Response;
    try {
      res = await this.fetchFn(this.baseUrl.replace(/\/$/, "") + path, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(this.timeoutMs),
      });
    } catch (err) {
      throw new ShfdError(502, "unreachable", `shfd is not reachable: ${(err as Error).message}`);
    }
    const text = await res.text();
    let json: unknown;
    try {
      json = text ? JSON.parse(text) : undefined;
    } catch {
      json = undefined;
    }
    if (!res.ok) {
      const e = (json as { error?: { code?: string; message?: string } } | undefined)?.error;
      throw new ShfdError(res.status, e?.code ?? "internal", e?.message ?? `shfd answered ${res.status}`);
    }
    return json as T;
  }

  private share(id: string): string {
    return `/v1/shares/${encodeURIComponent(id)}`;
  }

  whoami(token: string): Promise<Whoami> {
    return this.call("GET", "/v1/whoami", token);
  }
  health(): Promise<Health> {
    return this.call("GET", "/healthz", undefined);
  }
  stats(token: string, share: string): Promise<Stats> {
    return this.call("GET", `${this.share(share)}/admin/stats`, token);
  }
  members(token: string, share: string): Promise<MemberList> {
    return this.call("GET", `${this.share(share)}/admin/members`, token);
  }
  addMember(token: string, share: string, username: string, admin: boolean): Promise<TokenResponse> {
    return this.call("POST", `${this.share(share)}/admin/members`, token, { username, admin });
  }
  revoke(token: string, share: string, username: string): Promise<{ member: MemberInfo }> {
    return this.call("POST", `${this.share(share)}/admin/members/${encodeURIComponent(username)}/revoke`, token, {});
  }
  setRole(token: string, share: string, username: string, role: string): Promise<{ member: MemberInfo; old_role: string }> {
    return this.call("POST", `${this.share(share)}/admin/members/${encodeURIComponent(username)}/role`, token, { role });
  }
  resetToken(token: string, share: string, username: string): Promise<TokenResponse> {
    return this.call("POST", `${this.share(share)}/admin/members/${encodeURIComponent(username)}/reset-token`, token, {});
  }
  log(token: string, share: string, after: number | undefined, limit: number): Promise<LogPage> {
    const q = new URLSearchParams({ limit: String(limit) });
    if (after !== undefined) q.set("after", String(after));
    return this.call("GET", `${this.share(share)}/admin/log?${q}`, token);
  }
  files(token: string, share: string, dir: string): Promise<DirListing> {
    return this.call("GET", `${this.share(share)}/admin/files?dir=${encodeURIComponent(dir)}`, token);
  }
  trash(token: string, share: string): Promise<TrashList> {
    return this.call("GET", `${this.share(share)}/admin/trash`, token);
  }
  restore(token: string, share: string, id: number): Promise<RestoreResult> {
    return this.call("POST", `${this.share(share)}/admin/trash/${id}/restore`, token, {});
  }

  /**
   * One thumbnail, not read as JSON: the status (200, 304, 404, …), the
   * headers and the bytes. A 401 / 403 is a ShfdError, as for every call,
   * so the caller ends the session the same way.
   */
  async thumb(token: string, share: string, hash: string, size: ThumbSize, ifNoneMatch?: string): Promise<ThumbResponse> {
    const headers: Record<string, string> = { accept: "image/jpeg", authorization: `Bearer ${token}` };
    if (ifNoneMatch) headers["if-none-match"] = ifNoneMatch;
    const url = `${this.baseUrl.replace(/\/$/, "")}${this.share(share)}/admin/thumbs/${encodeURIComponent(hash)}?size=${size}`;
    let res: Response;
    try {
      res = await this.fetchFn(url, { method: "GET", headers, signal: AbortSignal.timeout(this.timeoutMs) });
    } catch (err) {
      throw new ShfdError(502, "unreachable", `shfd is not reachable: ${(err as Error).message}`);
    }
    const body = new Uint8Array(await res.arrayBuffer());
    if (res.status === 401 || res.status === 403) {
      let e: { code?: string; message?: string } | undefined;
      try {
        e = JSON.parse(new TextDecoder().decode(body))?.error;
      } catch {
        e = undefined;
      }
      throw new ShfdError(res.status, e?.code ?? (res.status === 401 ? "unauthorized" : "forbidden"), e?.message ?? `shfd answered ${res.status}`);
    }
    return { status: res.status, headers: res.headers, body };
  }
}

/** Serbian sentences for shfd's error codes; the UI never shows a bare code. */
export function errorMessage(code: string, fallback?: string): string {
  switch (code) {
    case "member_exists":
      return "Član sa tim imenom već postoji.";
    case "last_admin":
      return "To je poslednji aktivni admin foldera. Prvo dodeli ulogu admin nekom drugom.";
    case "unknown_member":
      return "Taj član ne postoji ili je opozvan.";
    case "invalid_request":
      return "Neispravan zahtev" + (fallback ? `: ${fallback}` : ".");
    case "unauthorized":
      return "Token nije ispravan ili je opozvan.";
    case "forbidden":
      return "Samo admin foldera može da koristi GUI.";
    case "unknown_trash":
      return "Taj unos više nije u trash-u.";
    case "trash_damaged":
      return "Fajl ovog unosa nedostaje ili je izmenjen; pokreni shfd verify.";
    case "dest_exists":
      return "Na toj putanji već postoji drugi fajl.";
    case "not_found":
      return "Ne postoji.";
    case "kind_mismatch":
      return "To je fajl, a ne folder.";
    case "invalid_path":
      return "Neispravna putanja.";
    case "unreachable":
      return "shfd ne odgovara.";
    default:
      return fallback ? `Greška: ${fallback}` : "Nešto nije uspelo.";
  }
}
