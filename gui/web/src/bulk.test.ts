import type { FilterNode, Row } from "@jsudc/core";
import type { RowSelection } from "@jsudc/react";
import { describe, expect, it, vi } from "vitest";
import { srPlural } from "../../shared/bulk";
import { chunked, inChunks, pageStateOf, rangeIds, removeItems, removeOrder, selectableOnly, selectedRows, sumRemove, sumRestore, type ListClient } from "./bulk";

const row = (id: string, kind = "file", extra: Record<string, unknown> = {}) =>
  ({ __meta: { id, version: 1 }, name: id === ".." ? ".." : id.split("/").pop(), path: id === ".." ? "" : id, kind, ...extra }) as unknown as Row;

/** A RowSelection over a plain set, as jsudc's id mode keeps it. */
function fakeSelection(initial: string[] = [], mode: "ids" | "matching" = "ids") {
  let ids = new Set(initial);
  const calls: string[] = [];
  const sel = (): RowSelection => ({
    mode,
    ids: [...ids],
    exceptIds: [],
    count: ids.size,
    isEmpty: ids.size === 0,
    rows: [...ids].map((id) => row(id)),
    pageState: "none",
    isSelected: (id) => ids.has(id),
    toggle: (id, opts) => {
      calls.push(`toggle ${id}${opts?.range ? " range" : ""}`);
      if (ids.has(id)) ids.delete(id);
      else ids.add(id);
    },
    setPage: (on) => calls.push(`setPage ${on}`),
    selectAllMatching: () => calls.push("all"),
    setIds: (next) => {
      calls.push(`setIds ${[...next].join(",")}`);
      ids = new Set(next);
    },
    clear: () => (ids = new Set()),
  });
  return { sel, calls, ids: () => [...ids] };
}

const loaded = [row("..", "dir"), row("docs/sub", "dir"), row("docs/a.txt"), row("docs/b.txt")];

describe("selectableOnly: the .. row cannot be selected", () => {
  it("toggle('..') does nothing; other rows toggle (with the range)", () => {
    const f = fakeSelection();
    const s = selectableOnly(f.sel(), loaded);
    s.toggle("..");
    expect(f.calls).toEqual([]);
    s.toggle("docs/a.txt", { range: true });
    expect(f.calls).toEqual(["toggle docs/a.txt range"]);
  });

  it("setPage(true) in id mode selects the loaded rows without .., keeping what was selected", () => {
    const f = fakeSelection(["elsewhere.txt"]);
    selectableOnly(f.sel(), loaded).setPage(true);
    expect(f.ids().sort()).toEqual(["docs/a.txt", "docs/b.txt", "docs/sub", "elsewhere.txt"]);
    expect(f.ids()).not.toContain("..");
  });

  it("setPage(false) and setPage in matching mode go to jsudc", () => {
    const f = fakeSelection(["docs/a.txt"]);
    selectableOnly(f.sel(), loaded).setPage(false);
    const m = fakeSelection([], "matching");
    selectableOnly(m.sel(), loaded).setPage(true);
    expect(f.calls).toEqual(["setPage false"]);
    expect(m.calls).toEqual(["setPage true"]);
  });

  it("pageState counts the rows without ..", () => {
    expect(selectableOnly(fakeSelection().sel(), loaded).pageState).toBe("none");
    expect(selectableOnly(fakeSelection(["docs/a.txt"]).sel(), loaded).pageState).toBe("some");
    expect(selectableOnly(fakeSelection(["docs/sub", "docs/a.txt", "docs/b.txt"]).sel(), loaded).pageState).toBe("all");
    expect(pageStateOf([row("..", "dir")], () => true)).toBe("none");
  });

  it("isSelected('..') is false, also in matching mode; ids, rows and setIds drop it", () => {
    const f = fakeSelection(["..", "docs/a.txt"], "matching");
    const s = selectableOnly(f.sel(), loaded);
    expect(s.isSelected("..")).toBe(false);
    expect(s.isSelected("docs/a.txt")).toBe(true);
    expect(s.ids).toEqual(["docs/a.txt"]);
    expect(s.rows.map((r) => r.__meta.id)).toEqual(["docs/a.txt"]);
    s.setIds(["..", "docs/b.txt"]);
    expect(f.ids()).toEqual(["docs/b.txt"]);
  });
});

describe("rangeIds: the range in the order drawn", () => {
  const order = [row("..", "dir"), row("z-dir", "dir"), row("a.txt"), row("b.txt"), row("c.txt")];
  it("from the anchor to the row, either way, without ..", () => {
    expect(rangeIds(order, "a.txt", "c.txt")).toEqual(["a.txt", "b.txt", "c.txt"]);
    expect(rangeIds(order, "c.txt", "z-dir")).toEqual(["z-dir", "a.txt", "b.txt", "c.txt"]);
    expect(rangeIds(order, "b.txt", "..")).toEqual(["z-dir", "a.txt", "b.txt"]);
  });
  it("no anchor (or one not drawn): only the row; never ..", () => {
    expect(rangeIds(order, null, "b.txt")).toEqual(["b.txt"]);
    expect(rangeIds(order, "gone.txt", "b.txt")).toEqual(["b.txt"]);
    expect(rangeIds(order, null, "..")).toEqual([]);
  });
});

/** A client over one folder's listing, paged as FilesSource pages it (the head first, counted in the total). */
function fakeClient(rows: Row[], head: Row[] = []) {
  const all = [...head, ...rows];
  const list = vi.fn(async (req: { collection: string; filter?: FilterNode; page?: { limit?: number; offset?: number } }) => {
    const o = req.page?.offset ?? 0;
    const l = req.page?.limit ?? 50;
    return { rows: all.slice(o, o + l), total: all.length };
  });
  return { list } satisfies ListClient;
}

describe("selectedRows", () => {
  const sub: FilterNode = { and: [{ field: "path", op: "eq", value: "docs" }, { field: "name", op: "contains", value: "a" }, { field: "ext", op: "eq", value: "txt" }] };

  it("id mode: the selected rows, without ..; no listing", async () => {
    const c = fakeClient([]);
    const out = await selectedRows(c, { collection: "files", rows: [row(".."), row("docs/a.txt")] });
    expect(out.map((r) => r.__meta.id)).toEqual(["docs/a.txt"]);
    expect(c.list).not.toHaveBeenCalled();
  });

  it("all that match: every page with the view's filter (folder, search, chip), minus exceptIds and ..", async () => {
    const files = Array.from({ length: 450 }, (_, i) => row(`docs/f${i}.txt`));
    const c = fakeClient(files, [row("..", "dir")]);
    const out = await selectedRows(c, { collection: "files", filter: sub, exceptIds: ["docs/f3.txt", "docs/f449.txt"], rows: [] });
    expect(out).toHaveLength(448);
    expect(out.map((r) => r.__meta.id)).not.toContain("..");
    expect(out.map((r) => r.__meta.id)).not.toContain("docs/f3.txt");
    expect(c.list.mock.calls.map(([req]) => req.page)).toEqual([
      { limit: 200, offset: 0 },
      { limit: 200, offset: 200 },
      { limit: 200, offset: 400 },
    ]);
    // The filter goes as the view composed it: the subfolder, not the root.
    for (const [req] of c.list.mock.calls) expect(req).toMatchObject({ collection: "files", filter: sub });
  });

  it("all that match, exactly one full page: stops on the total", async () => {
    const c = fakeClient(Array.from({ length: 200 }, (_, i) => row(`f${i}`)));
    const out = await selectedRows(c, { collection: "files", filter: { and: [] }, rows: [] });
    expect(out).toHaveLength(200);
    expect(c.list).toHaveBeenCalledTimes(1);
  });
});

describe("remove items, order and chunks", () => {
  it("a file carries its version, a folder only its path; .. never", () => {
    expect(removeItems([row("..", "dir"), row("docs", "dir", { hash_full: "" }), row("a.txt", "file", { hash_full: "ab" })])).toEqual([
      { path: "docs", kind: "dir", base_hash: "" },
      { path: "a.txt", kind: "file", base_hash: "ab" },
    ]);
  });
  it("files first, then folders deepest first", () => {
    const items = removeOrder([
      { path: "a", kind: "dir", base_hash: "" },
      { path: "x.txt", kind: "file", base_hash: "1" },
      { path: "a/b", kind: "dir", base_hash: "" },
      { path: "y.txt", kind: "file", base_hash: "2" },
    ]);
    expect(items.map((i) => i.path)).toEqual(["x.txt", "y.txt", "a/b", "a"]);
  });
  it("chunks of at most 500", () => {
    expect(chunked(Array.from({ length: 1201 }, (_, i) => i)).map((c) => c.length)).toEqual([500, 500, 201]);
    expect(chunked([])).toEqual([]);
  });
  it("inChunks: a call that fails stops the rest and keeps what came before", async () => {
    const call = vi.fn(async (c: number[]) => {
      if (c[0] === 500) throw new Error("gone");
      return c.length;
    });
    const r = await inChunks(Array.from({ length: 1200 }, (_, i) => i), call);
    expect(r.parts).toEqual([500]);
    expect((r.error as Error).message).toBe("gone");
    expect(call).toHaveBeenCalledTimes(2);
  });
});

describe("summaries of several calls", () => {
  it("remove: the counts added up, one sentence", () => {
    const r = sumRemove(
      [
        { removed: 500, unchanged: 0, failed: [] },
        { removed: 98, unchanged: 0, failed: [{ path: "x", message: "Folder nije prazan." }, { path: "y", message: "Izmenjen." }] },
      ],
      "sr",
    );
    expect(r).toMatchObject({ removed: 598, unchanged: 0 });
    expect(r.failed.map((f) => f.path)).toEqual(["x", "y"]);
    expect(r.message).toBe("Obrisano 598 od 600; 2 nije moglo.");
    expect(sumRemove([{ removed: 12, unchanged: 0, failed: [] }], "sr").message).toBe("Obrisano: 12 stavki; fajlovi se mogu vratiti iz Trash-a.");
    expect(sumRemove([{ removed: 1, unchanged: 0, failed: [] }], "en").message).toBe("Deleted 1 item; files can be restored from the Trash.");
    expect(sumRemove([{ removed: 0, unchanged: 0, failed: [{ path: "x", message: "m" }] }], "sr").message).toBe("Ništa nije obrisano; 1 od 1 nije moglo.");
  });
  it("restore: restored, renamed, failed", () => {
    const r = sumRestore(
      [
        { restored: 3, renamed: 1, unchanged: 0, failed: [] },
        { restored: 2, renamed: 1, unchanged: 1, failed: [{ id: "9", message: "Ne postoji." }] },
      ],
      "sr",
    );
    expect(r).toMatchObject({ restored: 5, renamed: 2, unchanged: 1 });
    expect(r.message).toBe("Vraćeno: 5 stavki (2 pod imenom .restored-…); 1 već ima taj sadržaj; 1 nije moglo.");
    expect(sumRestore([{ restored: 1, renamed: 0, unchanged: 0, failed: [] }], "en").message).toBe("Restored 1 item.");
  });
  it("Serbian plural forms", () => {
    expect([1, 2, 4, 5, 11, 12, 14, 21, 22, 25, 101, 112].map((n) => srPlural(n, "a", "e", "i")).join("")).toBe("aeeiiiiaeiai");
  });
});
