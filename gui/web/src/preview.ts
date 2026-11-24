// What the preview of a file shows, decided by its extension the same way
// shfd decides what it serves inline (internal/server/httpapi/content.go):
// images, PDF and plain text; everything else only downloads. Pure
// functions, the component is FilePreview.tsx.

import { BASE } from "./api";

export type PreviewKind = "image" | "text" | "pdf" | "none";

const KINDS: Record<string, PreviewKind> = {
  jpg: "image",
  jpeg: "image",
  png: "image",
  gif: "image",
  webp: "image",
  pdf: "pdf",
  txt: "text",
  md: "text",
  csv: "text",
  log: "text",
  json: "text",
};

/** The preview of a file of this name; a folder has none. */
export function previewKind(name: string, kind = "file"): PreviewKind {
  if (kind !== "file") return "none";
  const i = name.lastIndexOf(".");
  if (i <= 0 || i === name.length - 1) return "none";
  return KINDS[name.slice(i + 1).toLowerCase()] ?? "none";
}

/** How much of a text file the preview reads. */
export const TEXT_PREVIEW_BYTES = 200 * 1024;

/** The URL of a file's content through the GUI server. */
export function contentUrl(path: string, download = false): string {
  const q = new URLSearchParams({ path });
  if (download) q.set("download", "1");
  return `${BASE}/api/files/content?${q}`;
}

/**
 * The text of the first bytes of a file: UTF-8, invalid bytes as U+FFFD,
 * and a character cut by the end of the range dropped rather than shown
 * as garbage. `cut` says the file goes on after these bytes.
 */
export function decodeTextPreview(bytes: Uint8Array, total: number): { text: string; cut: boolean } {
  const cut = total > bytes.length;
  const text = new TextDecoder("utf-8").decode(bytes, { stream: cut });
  return { text, cut };
}
