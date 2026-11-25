import { describe, expect, it } from "vitest";
import { copyKeys, copyText } from "./clipboard";

describe("copyText", () => {
  it("is true when the clipboard took the text", async () => {
    let got = "";
    expect(await copyText("shf join", { writeText: async (t) => void (got = t) })).toBe(true);
    expect(got).toBe("shf join");
  });
  it("is false without a clipboard (a page over http)", async () => {
    expect(await copyText("x", undefined)).toBe(false);
  });
  it("is false when the browser refuses, and does not throw", async () => {
    expect(await copyText("x", { writeText: () => Promise.reject(new DOMException("denied", "NotAllowedError")) })).toBe(false);
  });
});

describe("copyKeys", () => {
  it("⌘C on macOS, Ctrl+C elsewhere", () => {
    expect(copyKeys("macos")).toBe("⌘C");
    expect(copyKeys("linux")).toBe("Ctrl+C");
    expect(copyKeys("windows")).toBe("Ctrl+C");
  });
});
