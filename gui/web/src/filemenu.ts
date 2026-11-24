// What the context menu of a row of the Files page offers, and the words
// of the info dialog. Pure functions; the menu itself is FileMenu.tsx.

import type { Row } from "@jsudc/core";
import { fileKind, type FileKind } from "./filekind";
import { previewKind } from "./preview";

export type MenuAction = "open" | "info" | "view" | "download" | "rename" | "remove";

export interface MenuItem {
  action: MenuAction;
  label: string;
  /** Shown, but not to be chosen; `hint` says why. */
  disabled?: boolean;
  hint?: string;
  danger?: boolean;
  /** A line above the item. */
  separator?: boolean;
}

const str = (v: unknown) => (v === null || v === undefined ? "" : String(v));

/**
 * The menu of a row: a file gets Informacije, Prikaži, Preuzmi, Preimenuj
 * and Obriši; a folder Otvori, Informacije and Obriši (shfd removes only an
 * empty folder, and renames only files); ".." gets none.
 */
export function menuItems(row: Row): MenuItem[] {
  const name = str(row.name);
  if (row.kind === "dir") {
    if (name === "..") return [];
    return [
      { action: "open", label: "Otvori" },
      { action: "info", label: "Informacije" },
      { action: "remove", label: "Obriši", danger: true, separator: true, hint: "Briše se samo prazan folder." },
    ];
  }
  const view = previewKind(name, str(row.kind));
  return [
    { action: "info", label: "Informacije" },
    view === "none"
      ? { action: "view", label: "Prikaži", disabled: true, hint: "Za ovaj tip fajla nema pregleda; preuzmi ga." }
      : { action: "view", label: "Prikaži" },
    { action: "download", label: "Preuzmi" },
    { action: "rename", label: "Preimenuj", separator: true },
    { action: "remove", label: "Obriši", danger: true },
  ];
}

const KIND_LABELS: Record<FileKind, string> = {
  folder: "Folder",
  image: "Slika",
  pdf: "PDF dokument",
  word: "Tekstualni dokument (Word)",
  excel: "Tabela (Excel)",
  powerpoint: "Prezentacija (PowerPoint)",
  text: "Tekst",
  code: "Kod",
  archive: "Arhiva",
  audio: "Zvuk",
  video: "Video",
  other: "Fajl",
};

/** The type of a row in words, with its extension: "Slika (JPG)". */
export function kindLabel(row: Row): string {
  const name = str(row.name);
  const k = fileKind(name, str(row.kind));
  const i = name.lastIndexOf(".");
  const ext = k !== "folder" && i > 0 && i < name.length - 1 ? name.slice(i + 1).toUpperCase() : "";
  return ext ? `${KIND_LABELS[k]} (${ext})` : KIND_LABELS[k];
}

/**
 * The part of a name the rename dialog selects: the name without its
 * extension, so typing replaces it and keeps ".jpg"; a name without one
 * (or a dot file) is selected whole.
 */
export function stemRange(name: string): [number, number] {
  const i = name.lastIndexOf(".");
  return i > 0 ? [0, i] : [0, name.length];
}

/**
 * Where a menu opened at (x, y) goes so that all of it stays on screen:
 * left of the pointer when it would leave on the right, above it when it
 * would leave at the bottom.
 */
export function placeMenu(x: number, y: number, w: number, h: number, vw: number, vh: number, margin = 8): { left: number; top: number } {
  const left = x + w + margin > vw ? Math.max(margin, x - w) : x;
  const top = y + h + margin > vh ? Math.max(margin, y - h) : y;
  return { left, top };
}

/** The keyboard's way to a context menu: the menu key, or Shift+F10. */
export function isMenuKey(e: { key: string; shiftKey: boolean }): boolean {
  return e.key === "ContextMenu" || (e.key === "F10" && e.shiftKey);
}
