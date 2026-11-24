import { afterEach, describe, expect, it } from "vitest";
import { ago, bytes, joinCommand, num } from "./format.js";
import { setLang } from "./lang.js";

describe("format", () => {
  afterEach(() => setLang("sr"));
  it("ago", () => {
    const now = Date.parse("2026-11-24T12:00:00Z");
    expect(ago("2026-11-24T11:59:48Z", now)).toBe("pre 12 s");
    expect(ago("2026-11-24T11:57:00Z", now)).toBe("pre 3 min");
    expect(ago("2026-11-22T12:00:00Z", now)).toBe("pre 2 dana");
    expect(ago("2026-11-03T12:00:00Z", now)).toBe("pre 3 ned.");
    expect(ago(null, now)).toBe("—");
  });
  it("bytes", () => {
    expect(bytes(512)).toBe("512 B");
    expect(bytes(1.24 * 1024 ** 3 * 10)).toBe("12,4 GB");
  });
  it("in English", () => {
    setLang("en");
    const now = Date.parse("2026-11-24T12:00:00Z");
    expect(ago("2026-11-24T11:59:48Z", now)).toBe("12 s ago");
    expect(ago("2026-11-22T12:00:00Z", now)).toBe("2 days ago");
    expect(ago("2026-11-17T12:00:00Z", now)).toBe("1 week ago");
    expect(bytes(1.24 * 1024 ** 3 * 10)).toBe("12.4 GB");
    expect(num(1234567)).toBe("1,234,567");
  });
  it("join command", () => {
    expect(joinCommand("https://h", "id", "shf_x", "projekti tim")).toBe("shf join https://h id shf_x ~/projekti-tim");
  });
});
