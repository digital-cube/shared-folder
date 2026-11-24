import type { Row } from "@jsudc/core";
import { describe, expect, it } from "vitest";
import { gridOrder, pendingKey } from "./liveThumbs";
import { clampSize, THUMB_DEFAULT, THUMB_MAX, THUMB_MIN } from "./thumbsize";

const row = (name: string, kind: string, thumb = "", hash = ""): Row =>
  ({ __meta: { id: name, version: 1 }, name, kind, thumb, hash_full: hash }) as Row;

describe("pendingKey", () => {
  it("is empty without pending rows, so nothing polls", () => {
    expect(pendingKey([])).toBe("");
    expect(pendingKey([row("a.jpg", "file", "ready", "a"), row("b.pdf", "file", "failed", "b"), row("docs", "dir")])).toBe("");
  });
  it("names the pending hashes in a stable order", () => {
    const rows = [row("b.jpg", "file", "pending", "bb"), row("a.jpg", "file", "pending", "aa"), row("c.jpg", "file", "ready", "cc")];
    expect(pendingKey(rows)).toBe("aa,bb");
    expect(pendingKey([...rows].reverse())).toBe("aa,bb");
  });
});

describe("gridOrder", () => {
  it("puts .. first and folders before files, keeping each group's order", () => {
    const rows = [row("z.txt", "file"), row("b", "dir"), row("a.jpg", "file"), row("..", "dir"), row("a", "dir")];
    const out = gridOrder(rows, (r) => r.kind === "dir").map((r) => r.name);
    expect(out).toEqual(["..", "b", "a", "z.txt", "a.jpg"]);
  });
});

describe("clampSize", () => {
  it("keeps the slider's range and falls back to the default", () => {
    expect(clampSize(144)).toBe(144);
    expect(clampSize("200")).toBe(200);
    expect(clampSize(10)).toBe(THUMB_MIN);
    expect(clampSize(1000)).toBe(THUMB_MAX);
    expect(clampSize("abc")).toBe(THUMB_DEFAULT);
    expect(clampSize(null)).toBe(THUMB_DEFAULT);
    expect(clampSize("")).toBe(THUMB_DEFAULT);
  });
});
