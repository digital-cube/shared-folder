import { afterEach, describe, expect, it } from "vitest";
import { getLang, htmlLang, intlLocale, setLang, storedLang, tr } from "./lang.js";

describe("lang", () => {
  afterEach(() => setLang("sr"));
  it("is Serbian unless English was chosen", () => {
    expect(storedLang(() => null)).toBe("sr");
    expect(storedLang(() => "sr")).toBe("sr");
    expect(storedLang(() => "de")).toBe("sr");
    expect(storedLang(() => "en")).toBe("en");
  });
  it("tr follows setLang", () => {
    expect(tr("Odjava", "Sign out")).toBe("Odjava");
    setLang("en");
    expect(getLang()).toBe("en");
    expect(tr("Odjava", "Sign out")).toBe("Sign out");
    expect(intlLocale()).toBe("en-US");
    expect(htmlLang("en")).toBe("en");
    setLang("sr");
    expect(intlLocale()).toBe("sr-Latn-RS");
    expect(htmlLang("sr")).toBe("sr-Latn");
  });
});
