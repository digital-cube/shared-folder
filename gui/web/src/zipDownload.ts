// Preuzmi of the Files bar: what it downloads for a selection, as pure
// functions (the handler is in pages/Files.tsx). One file is that file;
// anything else is one ZIP the GUI server makes (shfd.files_zip, then
// GET <base>/api/files/zip — server/src/zip.ts).

import type { FilterNode, Row } from "@jsudc/core";
import { guiUrl } from "./api";
import { tooManyPaths, ZIP_MAX_PATHS, type ZipLang } from "../../shared/zip";

/**
 * The folder a list shows: the `path eq <dir>` condition of its filter, the
 * same one the server's takePath reads (collections.ts); "" for the root.
 */
export function listDir(node: FilterNode | null | undefined): string {
  const find = (n: FilterNode): string | null => {
    if ("field" in n) return n.field === "path" && n.op === "eq" && typeof n.value === "string" ? n.value : null;
    if ("not" in n) return null;
    const kids = ("and" in n ? n.and : "or" in n ? n.or : []) as FilterNode[];
    for (const k of kids) {
      const d = find(k);
      if (d !== null) return d;
    }
    return null;
  };
  return (node ? (find(node) ?? "") : "").replace(/^\/+|\/+$/g, "");
}

/** The view's filter as it keeps it in its state (the `f` parameter: base64url JSON). */
export function filterOfState(params: URLSearchParams): FilterNode | null {
  const raw = params.get("f");
  if (!raw) return null;
  try {
    const bin = atob(raw.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (raw.length % 4)) % 4));
    return JSON.parse(new TextDecoder().decode(Uint8Array.from(bin, (c) => c.charCodeAt(0)))) as FilterNode;
  } catch {
    return null;
  }
}

export type DownloadPlan =
  | { kind: "file"; path: string; name: string }
  | { kind: "zip"; dir: string; paths: string[] }
  | { kind: "error"; message: string };

/**
 * What Preuzmi does with the selected rows of the folder `dir`: exactly
 * one file downloads itself; more, or a folder, is a ZIP; more paths than
 * a ZIP takes is a sentence, never a cut selection.
 */
export function downloadPlan(rows: readonly Row[], dir: string, lang: ZipLang): DownloadPlan {
  const picked = rows.filter((r) => String(r.__meta.id) !== "..");
  if (picked.length === 0) return { kind: "error", message: lang === "en" ? "Nothing is selected." : "Ništa nije izabrano." };
  if (picked.length === 1 && picked[0].kind === "file") return { kind: "file", path: String(picked[0].path ?? ""), name: String(picked[0].name ?? "") };
  if (picked.length > ZIP_MAX_PATHS) return { kind: "error", message: tooManyPaths(picked.length, lang) };
  return { kind: "zip", dir, paths: picked.map((r) => String(r.path ?? "")) };
}

/** The URL of a prepared ZIP through the GUI server. */
export function zipUrl(id: string, base?: string): string {
  return guiUrl(`/api/files/zip?${new URLSearchParams({ id })}`, base);
}

/** Starts a download by a hidden link: the browser streams it to disk. */
export function startDownload(href: string, name = ""): void {
  const a = document.createElement("a");
  a.href = href;
  a.download = name;
  a.rel = "noopener";
  a.style.display = "none";
  document.body.appendChild(a);
  a.click();
  a.remove();
}
