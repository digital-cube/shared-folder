// The selection of the Files page and what a bulk action does with it. The
// selection and its bar are jsudc's (`bulk_actions`, `UdcDataView actions`,
// `RendererProps.selection`); what is here is what the library leaves to
// the host, as pure functions:
//
// - the ".." row (the parent folder, FilesSource's head) is a row like any
//   other to jsudc: `selectableOnly` keeps it out of the selection;
// - in "all that match" mode a handler has a filter, not ids:
//   `selectedRows` lists the rows it stands for;
// - shfd.files_remove / shfd.trash_restore_many take at most BULK_MAX
//   items: `chunked` splits, `sumRemove` / `sumRestore` add the answers up.

import type { FilterNode, Row } from "@jsudc/core";
import type { BulkActionContext, RowSelection } from "@jsudc/react";
import { BULK_MAX, removeSummary, restoreSummary, type BulkLang } from "../../shared/bulk";
import type { RemoveItem } from "../../shared/actions";

/** The id of the parent folder's row (FilesSource's head). */
export const UP_ID = "..";

const idOf = (r: Row) => String(r.__meta.id);
const isUp = (id: string) => id === UP_ID;

/** How much of the rows is selected, the ".." row left out. */
export function pageStateOf(rows: readonly Row[], isSelected: (id: string) => boolean): RowSelection["pageState"] {
  const ids = rows.map(idOf).filter((id) => !isUp(id));
  if (ids.length === 0) return "none";
  const n = ids.filter(isSelected).length;
  return n === 0 ? "none" : n === ids.length ? "all" : "some";
}

/**
 * The view's selection as the table and the Grid get it: the ".." row can
 * be neither toggled nor selected with the page, and does not count in the
 * header checkbox. `rows` are the rows as loaded (`props.data.rows`).
 */
export function selectableOnly(sel: RowSelection, rows: readonly Row[]): RowSelection {
  const isSelected = (id: string) => !isUp(id) && sel.isSelected(id);
  return {
    ...sel,
    ids: sel.ids.filter((id) => !isUp(id)),
    rows: sel.rows.filter((r) => !isUp(idOf(r))),
    isSelected,
    pageState: pageStateOf(rows, isSelected),
    toggle: (id, opts) => {
      if (!isUp(id)) sel.toggle(id, opts);
    },
    setPage: (on) => {
      if (on && sel.mode === "ids") {
        // The loaded rows without "..", added to what is selected already.
        const ids = new Set(sel.ids);
        for (const r of rows) if (!isUp(idOf(r))) ids.add(idOf(r));
        sel.setIds([...ids]);
      } else sel.setPage(on);
    },
    setIds: (ids) => sel.setIds(ids.filter((id) => !isUp(id))),
  };
}

/**
 * The ids of a shift-click range in the order the rows are drawn (the Grid
 * draws folders first: gridOrder), from `anchor` to `id`, ".." left out.
 * Without the anchor among them: just `id`.
 */
export function rangeIds(order: readonly Row[], anchor: string | null, id: string): string[] {
  const ids = order.map(idOf);
  const a = anchor === null ? -1 : ids.indexOf(anchor);
  const b = ids.indexOf(id);
  if (a < 0 || b < 0) return isUp(id) ? [] : [id];
  const [lo, hi] = a < b ? [a, b] : [b, a];
  return ids.slice(lo, hi + 1).filter((x) => !isUp(x));
}

/** What `selectedRows` needs of the jsudc client. */
export interface ListClient {
  list(req: { collection: string; filter?: FilterNode; page?: { limit?: number; offset?: number } }): Promise<{ rows: Row[]; total: number | null }>;
}

const PAGE = 200;

/**
 * The rows a bulk action runs on, ".." never among them. Id mode: the
 * selected rows (`ctx.rows`). "All that match": every row of the
 * collection the filter matches (the folder, the search, the chips —
 * jsudc composes them all into `ctx.filter`), listed page by page, without
 * `ctx.exceptIds`.
 */
export async function selectedRows(client: ListClient, ctx: Pick<BulkActionContext, "collection" | "filter" | "exceptIds" | "rows">): Promise<Row[]> {
  if (!ctx.filter) return ctx.rows.filter((r) => !isUp(idOf(r)));
  const except = new Set(ctx.exceptIds ?? []);
  const seen = new Set<string>();
  const out: Row[] = [];
  for (let offset = 0; ; ) {
    const page = await client.list({ collection: ctx.collection, filter: ctx.filter, page: { limit: PAGE, offset } });
    for (const r of page.rows) {
      const id = idOf(r);
      if (isUp(id) || except.has(id) || seen.has(id)) continue;
      seen.add(id);
      out.push(r);
    }
    offset += page.rows.length;
    if (page.rows.length < PAGE || (page.total !== null && offset >= page.total)) return out;
  }
}

/** The items of shfd.files_remove: a file with the version shown, a folder by its path. */
export function removeItems(rows: readonly Row[]): RemoveItem[] {
  return rows
    .filter((r) => !isUp(idOf(r)))
    .map((r) =>
      r.kind === "dir"
        ? { path: String(r.path ?? ""), kind: "dir" as const, base_hash: "" }
        : { path: String(r.path ?? ""), kind: "file" as const, base_hash: String(r.hash_full ?? "") },
    );
}

/**
 * Files before folders, deeper folders before their parents: a folder
 * whose files are in the same selection is empty by the time it comes.
 */
export function removeOrder(items: readonly RemoveItem[]): RemoveItem[] {
  const depth = (p: string) => p.split("/").length;
  return [...items].sort((a, b) => (a.kind === b.kind ? (a.kind === "dir" ? depth(b.path) - depth(a.path) : 0) : a.kind === "file" ? -1 : 1));
}

/** The items in calls of at most `size`. */
export function chunked<T>(items: readonly T[], size = BULK_MAX): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size));
  return out;
}

export interface RemoveResult {
  removed: number;
  unchanged: number;
  failed: { path: string; message: string }[];
  message: string;
}

/** The answers of several shfd.files_remove calls as one, with one sentence. */
export function sumRemove(parts: readonly Omit<RemoveResult, "message">[], lang: BulkLang): RemoveResult {
  const removed = parts.reduce((n, p) => n + p.removed, 0);
  const unchanged = parts.reduce((n, p) => n + p.unchanged, 0);
  const failed = parts.flatMap((p) => p.failed);
  return { removed, unchanged, failed, message: removeSummary(lang, { removed, unchanged, failed: failed.length }) };
}

export interface RestoreResult {
  restored: number;
  renamed: number;
  unchanged: number;
  failed: { id: string; message: string }[];
  message: string;
}

/** The answers of several shfd.trash_restore_many calls as one, with one sentence. */
export function sumRestore(parts: readonly Omit<RestoreResult, "message">[], lang: BulkLang): RestoreResult {
  const restored = parts.reduce((n, p) => n + p.restored, 0);
  const renamed = parts.reduce((n, p) => n + p.renamed, 0);
  const unchanged = parts.reduce((n, p) => n + p.unchanged, 0);
  const failed = parts.flatMap((p) => p.failed);
  return { restored, renamed, unchanged, failed, message: restoreSummary(lang, { restored, renamed, unchanged, failed: failed.length }) };
}

/**
 * Runs the calls one after another. A call that fails as a whole (the
 * session ended, shfd not answering) stops the rest: `error` is set, and
 * `parts` still holds the answers of the calls before it.
 */
export async function inChunks<T, R>(items: readonly T[], call: (chunk: T[]) => Promise<R>): Promise<{ parts: R[]; error?: unknown }> {
  const parts: R[] = [];
  for (const c of chunked(items)) {
    try {
      parts.push(await call(c));
    } catch (error) {
      return { parts, error };
    }
  }
  return { parts };
}
