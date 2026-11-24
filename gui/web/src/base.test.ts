import { describe, expect, it } from "vitest";
import { guiUrl, readBase } from "./api";
import { contentUrl } from "./preview";
import { thumbUrl } from "./Thumb";

const H = "ab".repeat(32);

function doc(meta: string | null, pathname = "/"): Document {
  return {
    querySelector: (sel: string) => (meta !== null && sel === 'meta[name="shfd-gui-base"]' ? { getAttribute: () => meta } : null),
    location: { pathname },
  } as unknown as Document;
}

describe("readBase", () => {
  it("is the meta the server writes", () => {
    expect(readBase(doc(""))).toBe("");
    expect(readBase(doc("/"))).toBe("");
    expect(readBase(doc("/gui"))).toBe("/gui");
    expect(readBase(doc("/gui/", "/"))).toBe("/gui");
  });

  it("without the meta, is the directory of the page", () => {
    expect(readBase(doc(null, "/"))).toBe("");
    expect(readBase(doc(null, "/gui/"))).toBe("/gui");
    expect(readBase(doc(null, "/gui/index.html"))).toBe("/gui");
    expect(readBase(undefined)).toBe("");
  });
});

describe.each([
  { base: "", pre: "" },
  { base: "/gui", pre: "/gui" },
])("URLs of the web app with base '$base'", ({ base, pre }) => {
  it("the GUI's API", () => {
    expect(guiUrl("/api/session", base)).toBe(`${pre}/api/session`);
    expect(guiUrl("/api/login", base)).toBe(`${pre}/api/login`);
  });

  it("a file's content", () => {
    expect(contentUrl("docs/a b&c.txt", false, base)).toBe(`${pre}/api/files/content?path=docs%2Fa+b%26c.txt`);
    expect(contentUrl("čaj.pdf", true, base)).toBe(`${pre}/api/files/content?path=%C4%8Daj.pdf&download=1`);
  });

  it("a thumbnail", () => {
    expect(thumbUrl(H, "m", base)).toBe(`${pre}/api/thumb/${H}?size=m`);
  });
});
