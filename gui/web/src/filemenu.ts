// What the context menu of a row of the Files page offers, and the words
// of the info dialog. Pure functions; the menu itself is FileMenu.tsx.

import type { Row } from "@jsudc/core";
import { fileKind, type FileKind } from "./filekind";
import { tr } from "./lang";
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
 * empty folder, and renames only files); ".." gets none. Without canEdit
 * (a member who is not an admin) only the reading ones: no Preimenuj, no Obriši.
 */
export function menuItems(row: Row, canEdit = true): MenuItem[] {
  const all = allItems(row);
  return canEdit ? all : all.filter((i) => i.action !== "rename" && i.action !== "remove");
}

function allItems(row: Row): MenuItem[] {
  const name = str(row.name);
  if (row.kind === "dir") {
    if (name === "..") return [];
    return [
      { action: "open", label: tr("Otvori", "Open") },
      { action: "info", label: tr("Informacije", "Info") },
      {
        action: "remove",
        label: tr("Obriši", "Delete"),
        danger: true,
        separator: true,
        hint: tr("Briše se samo prazan folder.", "Only an empty folder can be deleted."),
      },
    ];
  }
  const view = previewKind(name, str(row.kind));
  return [
    { action: "info", label: tr("Informacije", "Info") },
    view === "none"
      ? {
          action: "view",
          label: tr("Prikaži", "View"),
          disabled: true,
          hint: tr("Za ovaj tip fajla nema pregleda; preuzmi ga.", "There is no preview for this type of file; download it."),
        }
      : { action: "view", label: tr("Prikaži", "View") },
    { action: "download", label: tr("Preuzmi", "Download") },
    { action: "rename", label: tr("Preimenuj", "Rename"), separator: true },
    { action: "remove", label: tr("Obriši", "Delete"), danger: true },
  ];
}

const KIND_LABELS: Record<FileKind, [sr: string, en: string]> = {
  folder: ["Folder", "Folder"],
  image: ["Slika", "Image"],
  pdf: ["PDF dokument", "PDF document"],
  word: ["Tekstualni dokument (Word)", "Text document (Word)"],
  excel: ["Tabela (Excel)", "Spreadsheet (Excel)"],
  powerpoint: ["Prezentacija (PowerPoint)", "Presentation (PowerPoint)"],
  text: ["Tekst", "Text"],
  code: ["Kod", "Code"],
  archive: ["Arhiva", "Archive"],
  audio: ["Zvuk", "Audio"],
  video: ["Video", "Video"],
  other: ["Fajl", "File"],
};

/** The type of a row in words, with its extension: "Slika (JPG)". */
export function kindLabel(row: Row): string {
  const name = str(row.name);
  const k = fileKind(name, str(row.kind));
  const i = name.lastIndexOf(".");
  const ext = k !== "folder" && i > 0 && i < name.length - 1 ? name.slice(i + 1).toUpperCase() : "";
  const label = tr(...KIND_LABELS[k]);
  return ext ? `${label} (${ext})` : label;
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
