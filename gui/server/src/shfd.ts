// The shfd HTTP API, always called with the token of the signed-in member,
// so shfd itself decides what that member may do (401 / 403).

import { tr, type Lang } from "./lang.js";

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

/** Headers of a request for a file's content that shfd answers itself. */
export const CONTENT_REQUEST_HEADERS = ["range", "if-range", "if-none-match"] as const;

/** How long shfd may take to start answering for a file's content: it
 * hashes a file it has not checked yet before the first byte. */
export const CONTENT_HEADERS_TIMEOUT_MS = 120_000;

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

/** One delete, move or rmdir of the sync protocol (POST /ops). */
export interface OpRequest {
  op: "delete" | "move" | "rmdir";
  path: string;
  new_path?: string;
  base_hash?: string;
}

/** What an op did: "applied", or "unchanged" when there was nothing to do. */
export interface OpResult {
  status: string;
  path: string;
  head: number;
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
  /** An op as the signed-in member, the same call a device makes. */
  op(token: string, share: string, req: OpRequest): Promise<OpResult> {
    return this.call("POST", `${this.share(share)}/ops`, token, req);
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

  /**
   * Opens the content of one file (GET /admin/files/content). The answer is
   * shfd's Response with its body not read, so the caller streams it; signal
   * aborts the download (the browser went away). Any refusal of shfd
   * (except 416) is a ShfdError with its code. Only the headers have a deadline
   * (CONTENT_HEADERS_TIMEOUT_MS); the body takes as long as it takes.
   */
  async content(
    token: string,
    share: string,
    path: string,
    opts: { download?: boolean; headers?: Record<string, string>; signal?: AbortSignal; headersTimeoutMs?: number } = {},
  ): Promise<Response> {
    const q = new URLSearchParams({ path });
    if (opts.download) q.set("download", "1");
    const url = `${this.baseUrl.replace(/\/$/, "")}/v1/shares/${encodeURIComponent(share)}/admin/files/content?${q}`;
    const ctl = new AbortController();
    const timer = setTimeout(() => ctl.abort(new Error("no answer from shfd in time")), opts.headersTimeoutMs ?? CONTENT_HEADERS_TIMEOUT_MS);
    const onAbort = () => ctl.abort(opts.signal?.reason);
    opts.signal?.addEventListener("abort", onAbort, { once: true });
    let res: Response;
    try {
      res = await this.fetchFn(url, { method: "GET", headers: { ...opts.headers, authorization: `Bearer ${token}` }, signal: ctl.signal });
    } catch (err) {
      opts.signal?.removeEventListener("abort", onAbort);
      throw new ShfdError(502, "unreachable", `shfd is not reachable: ${(err as Error).message}`);
    } finally {
      clearTimeout(timer);
    }
    if (res.status >= 400 && res.status !== 416) {
      opts.signal?.removeEventListener("abort", onAbort);
      let e: { code?: string; message?: string } | undefined;
      try {
        e = JSON.parse(await res.text())?.error;
      } catch {
        e = undefined;
      }
      throw new ShfdError(res.status, e?.code ?? (res.status === 401 ? "unauthorized" : res.status === 403 ? "forbidden" : "internal"), e?.message ?? `shfd answered ${res.status}`);
    }
    return res;
  }
}

/** Sentences for shfd's error codes, Serbian and English; the UI never shows a bare code. */
const ERROR_MESSAGES: Record<string, [sr: string, en: string]> = {
  member_exists: ["Član sa tim imenom već postoji.", "A member with that name already exists."],
  last_admin: [
    "To je poslednji aktivni admin foldera. Prvo dodeli ulogu admin nekom drugom.",
    "That is the folder's last active admin. Make someone else an admin first.",
  ],
  unknown_member: ["Taj član ne postoji ili je opozvan.", "That member does not exist or has been revoked."],
  unauthorized: ["Token nije ispravan ili je opozvan.", "The token is not valid or has been revoked."],
  forbidden: ["Samo admin foldera može da koristi GUI.", "Only an admin of the folder can use the GUI."],
  unknown_trash: ["Taj unos više nije u trash-u.", "That entry is no longer in the trash."],
  trash_damaged: [
    "Fajl ovog unosa nedostaje ili je izmenjen; pokreni shfd verify.",
    "The file of this entry is missing or has been changed; run shfd verify.",
  ],
  dest_exists: ["Na toj putanji već postoji drugi fajl.", "Another file already exists at that path."],
  not_found: ["Ne postoji.", "Does not exist."],
  kind_mismatch: ["To je fajl, a ne folder.", "That is a file, not a folder."],
  invalid_path: ["Neispravna putanja.", "Invalid path."],
  stale_base: [
    "Fajl je u međuvremenu izmenjen; osveži prikaz i pokušaj ponovo.",
    "The file has changed in the meantime; refresh the view and try again.",
  ],
  case_conflict: [
    "U folderu već postoji ime koje se razlikuje samo u velikim i malim slovima.",
    "The folder already has a name that differs only in upper and lower case.",
  ],
  is_dir: [
    "To je folder; folder se ne može preimenovati, a briše se tek kad je prazan.",
    "That is a folder; a folder cannot be renamed, and is removed only when it is empty.",
  ],
  dir_not_empty: ["Folder nije prazan; prvo obriši fajlove u njemu.", "The folder is not empty; remove the files in it first."],
  parent_not_dir: ["Na putanji je fajl, a ne folder.", "There is a file on the path, not a folder."],
  file_changed: [
    "Fajl na disku nije verzija koju baza beleži (nedostaje ili je izmenjen); pokreni shfd verify.",
    "The file on disk is not the version the database records (missing or changed); run shfd verify.",
  ],
  unreachable: ["shfd ne odgovara.", "shfd is not answering."],
};

/** The sentence for one of shfd's error codes, in the UI's language. */
export function errorMessage(code: string, fallback?: string, lang: Lang = "sr"): string {
  const m = ERROR_MESSAGES[code];
  if (m) return tr(lang, m[0], m[1]);
  if (code === "invalid_request") return tr(lang, "Neispravan zahtev", "Invalid request") + (fallback ? `: ${fallback}` : ".");
  if (fallback) return tr(lang, `Greška: ${fallback}`, `Error: ${fallback}`);
  return tr(lang, "Nešto nije uspelo.", "Something went wrong.");
}
