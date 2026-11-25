import type { Row } from "@jsudc/core";
import { describe, expect, it } from "vitest";
import { ZIP_MAX_PATHS } from "../../shared/zip";
import { contentUrl } from "./preview";
import { downloadPlan, filterOfState, listDir, zipUrl } from "./zipDownload";

const row = (path: string, kind: "file" | "dir" = "file"): Row => ({ __meta: { id: path }, path, name: path.split("/").pop(), kind }) as unknown as Row;
const up = { __meta: { id: ".." }, path: "", name: "..", kind: "dir" } as unknown as Row;

describe("downloadPlan: what Preuzmi downloads", () => {
  it("exactly one file: the file itself, no ZIP", () => {
    expect(downloadPlan([row("docs/a.txt")], "docs", "sr")).toEqual({ kind: "file", path: "docs/a.txt", name: "a.txt" });
    // ".." is never in the selection.
    expect(downloadPlan([up, row("docs/a.txt")], "docs", "sr")).toMatchObject({ kind: "file" });
  });

  it("more files, or a folder: one ZIP of the list's folder", () => {
    expect(downloadPlan([row("docs/a.txt"), row("docs/b.txt")], "docs", "sr")).toEqual({ kind: "zip", dir: "docs", paths: ["docs/a.txt", "docs/b.txt"] });
    expect(downloadPlan([row("docs/slike", "dir")], "docs", "sr")).toEqual({ kind: "zip", dir: "docs", paths: ["docs/slike"] });
    expect(downloadPlan([row("a.txt"), row("docs", "dir")], "", "en")).toEqual({ kind: "zip", dir: "", paths: ["a.txt", "docs"] });
  });

  it("more than a ZIP takes: a sentence, never a cut selection", () => {
    const many = Array.from({ length: ZIP_MAX_PATHS + 1 }, (_, i) => row(`f${i}`));
    const p = downloadPlan(many, "", "sr");
    expect(p.kind).toBe("error");
    expect(p.kind === "error" && p.message).toMatch(/nadređenog/);
    expect(downloadPlan(many.slice(0, ZIP_MAX_PATHS), "", "sr").kind).toBe("zip");
    expect(downloadPlan([], "", "en")).toEqual({ kind: "error", message: "Nothing is selected." });
  });
});

describe("the list's folder", () => {
  it("is the path condition of the filter, wherever it is", () => {
    expect(listDir(null)).toBe("");
    expect(listDir({ field: "path", op: "eq", value: "docs/slike" })).toBe("docs/slike");
    expect(listDir({ and: [{ field: "path", op: "eq", value: "docs" }, { field: "name", op: "contains", value: "a" }] })).toBe("docs");
    expect(listDir({ field: "name", op: "contains", value: "a" })).toBe("");
  });

  it("is read back from the view's state (f: base64url JSON, UTF-8)", () => {
    const tree = { and: [{ field: "path", op: "eq", value: "Škola/čćšžđ" }, { field: "ext", op: "in", value: ["txt"] }] };
    const bytes = new TextEncoder().encode(JSON.stringify(tree));
    const f = btoa(String.fromCharCode(...bytes)).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
    expect(listDir(filterOfState(new URLSearchParams({ f, tab: "thumbs" })))).toBe("Škola/čćšžđ");
    expect(filterOfState(new URLSearchParams())).toBeNull();
    expect(filterOfState(new URLSearchParams({ f: "!!" }))).toBeNull();
  });
});

describe("zipUrl", () => {
  it("goes through the GUI's base path, as contentUrl", () => {
    expect(zipUrl("abc_-1", "")).toBe("/api/files/zip?id=abc_-1");
    expect(zipUrl("abc_-1", "/gui")).toBe("/gui/api/files/zip?id=abc_-1");
    expect(contentUrl("a b.txt", true, "/gui")).toBe("/gui/api/files/content?path=a+b.txt&download=1");
  });
});
