// The rules of a ZIP download of the Files page (shfd.files_zip, then
// GET <base>/api/files/zip), shared by the GUI server and the browser.

/** At most this many paths per shfd.files_zip (the items selected in one folder). */
export const ZIP_MAX_PATHS = 2000;

/** At most this many entries (files and folders, with everything inside the folders) per archive. */
export const ZIP_MAX_ENTRIES = 20_000;

export type ZipLang = "sr" | "en";

const badSegment = (s: string) => s === "" || s === "." || s === ".." || s.includes("\0");

/** Is this a path of the folder as shfd writes it: "a/b.txt", never "/a", "a//b", "../a" or "a/."? */
export function isCleanPath(p: string): boolean {
  return p !== "" && !p.split("/").some(badSegment);
}

/**
 * What is wrong with a shfd.files_zip request, as a sentence; null when
 * nothing is. `dir` is the folder of the list ("" for the root) and every
 * path lies inside it.
 */
export function zipRequestProblem(dir: string, paths: readonly string[], lang: ZipLang): string | null {
  const tr = (sr: string, en: string) => (lang === "en" ? en : sr);
  if (dir !== "" && !isCleanPath(dir)) return tr(`Neispravan folder: ${dir}`, `Invalid folder: ${dir}`);
  if (paths.length === 0) return tr("Izaberi bar jedan fajl ili folder.", "Select at least one file or folder.");
  if (paths.length > ZIP_MAX_PATHS) return tooManyPaths(paths.length, lang);
  const prefix = dir === "" ? "" : `${dir}/`;
  for (const p of paths) {
    if (!isCleanPath(p)) return tr(`Neispravna putanja: ${p}`, `Invalid path: ${p}`);
    if (!p.startsWith(prefix) || p.length === prefix.length) return tr(`${p} nije u folderu ${dir || "/"}.`, `${p} is not in the folder ${dir || "/"}.`);
  }
  return null;
}

/** More than ZIP_MAX_PATHS selected: what to do instead (never cut silently). */
export function tooManyPaths(n: number, lang: ZipLang): string {
  return lang === "en"
    ? `${n} items are selected; a ZIP takes at most ${ZIP_MAX_PATHS} from one folder. Select fewer, or select the whole folder from its parent.`
    : `Izabrano je ${n} stavki; ZIP prima najviše ${ZIP_MAX_PATHS} iz jednog foldera. Izaberi manje ili ceo folder iz nadređenog foldera.`;
}

/** More than ZIP_MAX_ENTRIES in the archive. */
export function tooManyEntries(lang: ZipLang): string {
  return lang === "en"
    ? `The selection holds more than ${ZIP_MAX_ENTRIES} files and folders; a ZIP takes at most that many. Select less.`
    : `Izbor sadrži više od ${ZIP_MAX_ENTRIES} fajlova i foldera, a ZIP ih prima najviše toliko. Izaberi manje.`;
}

/** A path as it is in the archive: relative to `dir` ("docs/a.txt" from "docs" is "a.txt"). */
export function archivePath(dir: string, path: string): string {
  return dir === "" ? path : path.slice(dir.length + 1);
}

/** The archive's name: the folder of the list (the shared folder's at the root) + ".zip". */
export function zipName(dir: string, shareName: string): string {
  const last = dir === "" ? shareName : dir.slice(dir.lastIndexOf("/") + 1);
  // No path separators, quotes or control characters: it goes into Content-Disposition.
  // eslint-disable-next-line no-control-regex
  const clean = last.replace(/[\u0000-\u001f\u007f"\\/:*?<>|]+/g, "_").trim().replace(/^\.+/, "");
  return `${clean || "fajlovi"}.zip`;
}

/** `attachment` with an ASCII name and the UTF-8 one (RFC 6266 / 5987). */
export function attachmentDisposition(name: string): string {
  const ascii = name.replace(/[^\x20-\x7e]/g, "_").replace(/["\\]/g, "_");
  const utf8 = encodeURIComponent(name).replace(/['()*]/g, (c) => `%${c.charCodeAt(0).toString(16).toUpperCase()}`);
  return `attachment; filename="${ascii}"; filename*=UTF-8''${utf8}`;
}

/** The name of the entry listing what was skipped, at the end of the archive. */
export function skippedName(lang: ZipLang): string {
  return lang === "en" ? "_skipped.txt" : "_preskoceno.txt";
}

/** The text of that entry: one line per skipped path, with the reason. */
export function skippedText(skipped: readonly { path: string; reason: string }[], lang: ZipLang): string {
  const head =
    lang === "en"
      ? "These files are not in the archive: they were gone or refused while it was being made.\n\n"
      : "Ovi fajlovi nisu u arhivi: nestali su ili su odbijeni dok je pravljena.\n\n";
  return head + skipped.map((s) => `${s.path}\t${s.reason}`).join("\n") + "\n";
}
