// A ZIP of files and folders of the signed-in folder, made by the GUI
// server from shfd's content of one file at a time (shfd itself sends one
// file per request). Two steps, so the download is a plain link the browser
// streams to disk and the list of paths never goes into a URL:
//
// 1. shfd.files_zip (app.ts) checks the paths, walks the folders among them
//    (planZip) and keeps the plan here (ZipJobs): bound to the session and
//    the folder, used once, gone after ZIP_TTL_MS. The walk is done in this
//    step so that too large a selection is refused with a sentence the
//    page shows, before any download starts.
// 2. GET <base>/api/files/zip?id=… takes the plan and streams the archive
//    (zipStream): stored entries (no compression), one file after another,
//    with backpressure to the browser, ZIP64 where needed (yazl).
//
// A file shfd refuses on its own (gone since, not_found) is skipped and
// named in a text entry at the end; anything else (shfd unreachable, a
// stream cut halfway, the session ended) destroys the stream, so the
// browser reports a failed download instead of a ZIP that looks whole and
// is not.

import { randomBytes } from "node:crypto";
import { PassThrough, Readable } from "node:stream";
import type { ReadableStream as WebReadableStream } from "node:stream/web";
import { ZipFile } from "yazl";
import { archivePath, skippedName, skippedText, ZIP_MAX_ENTRIES, type ZipLang } from "../../shared/zip.js";
import type { FileEntry } from "./shfd.js";

/** How long a prepared ZIP waits for its download. */
export const ZIP_TTL_MS = 60_000;

/** Prepared ZIPs a session may have waiting; a new one pushes the oldest out. */
export const ZIP_PENDING_PER_SESSION = 4;

export interface ZipEntry {
  /** The path in the folder. */
  path: string;
  /** The path in the archive (relative to the list's folder). */
  arc: string;
  kind: "file" | "dir";
  mtime: Date;
}

export interface Skipped {
  path: string;
  reason: string;
}

export interface ZipPlan {
  entries: ZipEntry[];
  /** Selected, and no longer in the folder when the plan was made. */
  skipped: Skipped[];
}

export interface ZipJob extends ZipPlan {
  id: string;
  sessionId: string;
  shareId: string;
  name: string;
  lang: ZipLang;
  expires: number;
}

/** The plan is larger than ZIP_MAX_ENTRIES. */
export class TooManyEntries extends Error {}

/** A refusal of a listing that means the folder is gone (it is skipped, not an error). */
export type IsGone = (err: unknown) => string | null;

const parentOf = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");
const dateOf = (s: string | null | undefined) => {
  const d = s ? new Date(s) : new Date();
  return Number.isNaN(d.getTime()) ? new Date() : d;
};

/**
 * The entries of the archive: each selected path found in its folder's
 * listing, a folder with everything below it (its own entry too, so an
 * empty folder is in the archive). `list` is shfd's listing of a folder;
 * a listing refused as gone (`gone`) makes its path skipped. More than
 * `max` entries throws TooManyEntries as soon as it is known.
 */
export async function planZip(
  list: (dir: string) => Promise<FileEntry[]>,
  dir: string,
  paths: readonly string[],
  gone: IsGone,
  max = ZIP_MAX_ENTRIES,
): Promise<ZipPlan> {
  const entries: ZipEntry[] = [];
  const skipped: Skipped[] = [];
  const seen = new Set<string>();
  const listings = new Map<string, FileEntry[] | string>();
  const listOrGone = async (d: string): Promise<FileEntry[] | string> => {
    let l = listings.get(d);
    if (l === undefined) {
      try {
        l = await list(d);
      } catch (err) {
        const why = gone(err);
        if (why === null) throw err;
        l = why;
      }
      listings.set(d, l);
    }
    return l;
  };
  const add = (e: FileEntry) => {
    if (seen.has(e.path)) return false;
    seen.add(e.path);
    if (entries.length >= max) throw new TooManyEntries();
    entries.push({ path: e.path, arc: archivePath(dir, e.path), kind: e.kind, mtime: dateOf(e.mtime) });
    return true;
  };
  const walk = async (root: string) => {
    const stack = [root];
    while (stack.length > 0) {
      const d = stack.pop()!;
      const l = await listOrGone(d);
      if (typeof l === "string") continue; // gone since its parent was listed: its own entry stays
      for (const e of l) if (add(e) && e.kind === "dir") stack.push(e.path);
    }
  };
  for (const p of paths) {
    if (seen.has(p)) continue;
    const l = await listOrGone(parentOf(p));
    const e = typeof l === "string" ? undefined : l.find((x) => x.path === p);
    if (!e) {
      skipped.push({ path: p, reason: typeof l === "string" ? l : "not_found" });
      continue;
    }
    if (add(e) && e.kind === "dir") await walk(e.path);
  }
  return { entries, skipped };
}

/** The prepared ZIPs, in memory: one session's, one folder's, used once. */
export class ZipJobs {
  private readonly jobs = new Map<string, ZipJob>();
  constructor(
    private readonly now: () => number = Date.now,
    private readonly ttlMs = ZIP_TTL_MS,
    private readonly perSession = ZIP_PENDING_PER_SESSION,
  ) {}

  put(job: Omit<ZipJob, "id" | "expires">): ZipJob {
    this.sweep();
    const mine = [...this.jobs.values()].filter((j) => j.sessionId === job.sessionId);
    // Oldest first (a Map keeps the order of insertion).
    for (const j of mine.slice(0, Math.max(0, mine.length - this.perSession + 1))) this.jobs.delete(j.id);
    const full: ZipJob = { ...job, id: randomBytes(18).toString("base64url"), expires: this.now() + this.ttlMs };
    this.jobs.set(full.id, full);
    return full;
  }

  /** The job, when it is this session's and this folder's and not expired; it is gone after. */
  take(id: string, sessionId: string, shareId: string): ZipJob | undefined {
    const j = this.peek(id, sessionId, shareId);
    if (j) this.jobs.delete(id);
    return j;
  }

  /** As take, without using it up. */
  peek(id: string, sessionId: string, shareId: string): ZipJob | undefined {
    this.sweep();
    const j = this.jobs.get(id);
    return j && j.sessionId === sessionId && j.shareId === shareId ? j : undefined;
  }

  /** Every job of a session, when it ends. */
  dropSession(sessionId: string): void {
    for (const j of this.jobs.values()) if (j.sessionId === sessionId) this.jobs.delete(j.id);
  }

  size(): number {
    this.sweep();
    return this.jobs.size;
  }

  private sweep(): void {
    const t = this.now();
    for (const j of this.jobs.values()) if (j.expires <= t) this.jobs.delete(j.id);
  }
}

export interface ZipStreamOptions {
  /** shfd's content of one file (its body is streamed), or its refusal. */
  open: (path: string, signal: AbortSignal) => Promise<Response>;
  /** The reason a refused file is skipped, or null when the refusal ends the download. */
  skip: (err: unknown) => string | null;
  /** The browser left: stop reading from shfd. */
  signal: AbortSignal;
  lang: ZipLang;
  /** Called once with what ended the download early. */
  onFail?: (err: unknown) => void;
}

/**
 * The archive of a plan as a stream. Ends with the central directory only
 * when every entry went in (or was skipped and listed); otherwise the
 * stream is destroyed with the error.
 */
export function zipStream(plan: ZipPlan, o: ZipStreamOptions): Readable {
  const zip = new ZipFile();
  const out = new PassThrough();
  zip.outputStream.pipe(out);
  let failed = false;
  let current: Readable | undefined;
  const fail = (err: unknown) => {
    if (failed) return;
    failed = true;
    current?.destroy();
    zip.outputStream.unpipe(out);
    out.destroy(err instanceof Error ? err : new Error(String(err)));
    o.onFail?.(err);
  };
  zip.on("error", fail);
  out.on("close", () => {
    if (!out.readableEnded) fail(new Error("the download was closed"));
  });

  const run = async () => {
    const skipped = [...plan.skipped];
    const taken = new Set(plan.entries.map((e) => e.arc));
    for (const e of plan.entries) {
      if (failed) return;
      if (o.signal.aborted) throw o.signal.reason ?? new Error("aborted");
      if (e.kind === "dir") {
        zip.addEmptyDirectory(e.arc, { mtime: e.mtime });
        continue;
      }
      let res: Response;
      try {
        res = await o.open(e.path, o.signal);
      } catch (err) {
        const reason = o.skip(err);
        if (reason === null) throw err;
        skipped.push({ path: e.path, reason });
        continue;
      }
      if (res.status !== 200) {
        await res.body?.cancel().catch(() => {});
        throw new Error(`shfd answered ${res.status} for ${e.path}`);
      }
      if (!res.body) {
        zip.addBuffer(Buffer.alloc(0), e.arc, { mtime: e.mtime, compress: false });
        continue;
      }
      const body = Readable.fromWeb(res.body as WebReadableStream<Uint8Array>);
      current = body;
      // The next file is asked for once this one is read to its end:
      // one file from shfd at a time, at the pace the browser takes it.
      await new Promise<void>((resolve, reject) => {
        body.once("end", resolve);
        body.once("error", reject);
        body.once("close", () => (body.readableEnded ? resolve() : reject(new Error(`the content of ${e.path} was cut`))));
        zip.addReadStream(body, e.arc, { mtime: e.mtime, compress: false });
      });
      current = undefined;
    }
    if (failed) return;
    if (skipped.length > 0) {
      let name = skippedName(o.lang);
      for (let i = 2; taken.has(name); i++) name = skippedName(o.lang).replace(/\.txt$/, ` (${i}).txt`);
      zip.addBuffer(Buffer.from(skippedText(skipped, o.lang), "utf8"), name, { compress: false });
    }
    zip.end();
  };
  run().catch(fail);
  return out;
}
