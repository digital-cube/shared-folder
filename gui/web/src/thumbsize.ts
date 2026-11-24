// The size of the Grid's tiles (the slider in its toolbar) and the last tab
// of the Files view, both kept in localStorage. The size is a tiny external
// store, so moving the slider redraws the grid and loads no rows.

import { useSyncExternalStore } from "react";

export const THUMB_MIN = 64;
export const THUMB_MAX = 320;
export const THUMB_DEFAULT = 144;
export const THUMB_STEP = 8;
/** Below this the tiles show the name only, no size and date. */
export const THUMB_COMPACT_BELOW = 112;

const SIZE_KEY = "shfd-gui.thumb-size";
const TAB_KEY = "shfd-gui.files-tab";

function storage(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    return null; // storage blocked
  }
}

export function clampSize(v: unknown): number {
  const n = Math.round(Number(v));
  if (!Number.isFinite(n) || v === null || v === "") return THUMB_DEFAULT;
  return Math.min(THUMB_MAX, Math.max(THUMB_MIN, n));
}

let size = clampSize(storage()?.getItem(SIZE_KEY) ?? THUMB_DEFAULT);
const listeners = new Set<() => void>();

export function setThumbSize(v: number): void {
  const next = clampSize(v);
  if (next === size) return;
  size = next;
  try {
    storage()?.setItem(SIZE_KEY, String(next));
  } catch {
    /* full or blocked: the size still holds for this page */
  }
  for (const l of listeners) l();
}

export function useThumbSize(): number {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => size,
    () => size,
  );
}

/** The tab the Files view opened last ("table" or "thumbs"); null when none was kept. */
export function savedFilesTab(): string | null {
  const v = storage()?.getItem(TAB_KEY) ?? null;
  return v === "table" || v === "thumbs" ? v : null;
}

export function saveFilesTab(tab: string): void {
  try {
    storage()?.setItem(TAB_KEY, tab);
  } catch {
    /* ignore */
  }
}
