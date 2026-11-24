// While the rows on screen have a thumbnail that shfd thumbs has not made
// yet ("pending"), the list is loaded again every 3 s, so each one shows up
// on its own. At most 2 min for the same pending set of the same view; a
// new folder, search or page, or a change of which rows are pending, starts
// the 2 min again. No pending row: no extra requests at all.

import type { Row } from "@jsudc/core";
import type { UdcCollectionResult } from "@jsudc/react";
import { useEffect, useRef } from "react";

export const PENDING_POLL_MS = 3_000;
export const PENDING_POLL_MAX_MS = 120_000;

/** The Grid's order: `..` first, the folders next, then the files, each group as the list had it. */
export function gridOrder(rows: readonly Row[], isDir: (r: Row) => boolean): Row[] {
  const up = rows.filter((r) => r.name === ".." && isDir(r));
  const dirs = rows.filter((r) => r.name !== ".." && isDir(r));
  const files = rows.filter((r) => !isDir(r));
  return [...up, ...dirs, ...files];
}

/** The pending rows of a list, as one string: "" when none. */
export function pendingKey(rows: readonly Row[]): string {
  return rows
    .filter((r) => r.thumb === "pending")
    .map((r) => String(r.hash_full ?? r.__meta.id))
    .sort()
    .join(",");
}

export function usePendingThumbs(
  data: UdcCollectionResult,
  resetKey: string,
  { enabled = true, intervalMs = PENDING_POLL_MS, maxMs = PENDING_POLL_MAX_MS } = {},
): void {
  const pending = pendingKey(data.rows);
  const refetch = useRef(data.refetch);
  refetch.current = data.refetch;
  useEffect(() => {
    if (!enabled || !pending) return;
    const start = Date.now();
    const id = setInterval(() => {
      if (Date.now() - start >= maxMs) {
        clearInterval(id);
        return;
      }
      if (typeof document !== "undefined" && document.hidden) return; // a background tab waits
      refetch.current();
    }, intervalMs);
    return () => clearInterval(id);
  }, [enabled, pending, resetKey, intervalMs, maxMs]);
}
