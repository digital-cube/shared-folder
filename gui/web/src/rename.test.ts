import { describe, expect, it } from "vitest";
import { renameTarget } from "../../shared/rename";

describe("renameTarget", () => {
  it("keeps the folder and changes the name", () => {
    expect(renameTarget("a.txt", "b.txt")).toBe("b.txt");
    expect(renameTarget("docs/2026/plan.md", "  plan v2.md ")).toBe("docs/2026/plan v2.md");
  });
  it("refuses an empty name, . and .., and a slash or backslash", () => {
    for (const n of ["", "  ", ".", "..", "a/b", "a\\b", "x\u0000"]) {
      expect(typeof renameTarget("docs/a.txt", n)).toBe("object");
    }
  });
});
