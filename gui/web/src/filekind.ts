// What a row of the Files page is, for its icon, and which thumbnail size
// to ask shfd for. Pure functions: the components are in FileIcon.tsx and
// Thumb.tsx. The table of extensions is shared with the server
// (shared/filekinds.ts), which offers them in the extension chip.

import { EXTENSIONS } from "../../shared/filekinds";

export type FileKind =
  | "folder"
  | "image"
  | "pdf"
  | "word"
  | "excel"
  | "powerpoint"
  | "text"
  | "code"
  | "archive"
  | "audio"
  | "video"
  | "other";

const BY_EXT = new Map<string, FileKind>();
for (const [kind, exts] of Object.entries(EXTENSIONS)) for (const e of exts) BY_EXT.set(e, kind as FileKind);

/**
 * The kind of a row of the files collection, from its name and its `kind`
 * ("dir" or "file"). The extension is the part after the last dot, in any
 * case; a name without one, or a dot file (".bashrc"), is "other".
 */
export function fileKind(name: string, kind: string): FileKind {
  if (kind === "dir") return "folder";
  const i = name.lastIndexOf(".");
  if (i <= 0 || i === name.length - 1) return "other";
  return BY_EXT.get(name.slice(i + 1).toLowerCase()) ?? "other";
}

/** The sizes shfd makes, longer side in pixels, and their names in `?size=`. */
export const VARIANTS = [
  { px: 96, size: "s" },
  { px: 256, size: "m" },
  { px: 512, size: "l" },
] as const;
export type Variant = (typeof VARIANTS)[number]["size"];

/**
 * The thumbnail to draw a box of `cssPx` CSS pixels with on a screen of
 * `dpr` device pixels per CSS pixel: the smallest variant at least as big
 * as the box in device pixels, else the largest.
 */
export function pickVariant(cssPx: number, dpr = 1): Variant {
  const need = cssPx * (dpr > 0 && Number.isFinite(dpr) ? dpr : 1);
  for (const v of VARIANTS) if (v.px >= need) return v.size;
  return "l";
}
