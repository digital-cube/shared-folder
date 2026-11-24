import { describe, expect, it } from "vitest";
import { detectOs } from "./os";

describe("detectOs", () => {
  it("recognises macOS (Intel and Apple silicon report the same)", () => {
    expect(detectOs({ platform: "MacIntel", userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605.1.15" })).toBe("macos");
    expect(detectOs({ userAgentData: { platform: "macOS" } })).toBe("macos");
  });
  it("recognises Windows", () => {
    expect(detectOs({ platform: "Win32", userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/130.0" })).toBe("windows");
    expect(detectOs({ userAgentData: { platform: "Windows" } })).toBe("windows");
  });
  it("falls back to Linux", () => {
    expect(detectOs({ platform: "Linux x86_64", userAgent: "Mozilla/5.0 (X11; Linux x86_64) Chrome/130.0" })).toBe("linux");
    expect(detectOs({ platform: "", userAgent: "" })).toBe("linux");
    expect(detectOs(undefined)).toBe("linux");
  });
});
