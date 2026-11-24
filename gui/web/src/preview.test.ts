import { describe, expect, it } from "vitest";
import { contentUrl, decodeTextPreview, previewKind } from "./preview";

describe("previewKind", () => {
  it("images, PDF and plain text by extension, in any case", () => {
    expect(previewKind("hq720.jpg")).toBe("image");
    expect(previewKind("MZ9_7952.JPEG")).toBe("image");
    expect(previewKind("a.png")).toBe("image");
    expect(previewKind("a.gif")).toBe("image");
    expect(previewKind("a.webp")).toBe("image");
    expect(previewKind("Ugovor.PDF")).toBe("pdf");
    for (const n of ["test.txt", "README.md", "a.csv", "x.log", "c.json"]) expect(previewKind(n), n).toBe("text");
  });

  it("everything a browser would run, and everything else, only downloads", () => {
    for (const n of ["noi.html", "a.htm", "logo.svg", "a.xml", "app.js", "a.docx", "a.zip", "a.heic", "Makefile", ".bashrc", "kraj."]) {
      expect(previewKind(n), n).toBe("none");
    }
  });

  it("a folder has no preview", () => {
    expect(previewKind("slike.jpg", "dir")).toBe("none");
  });
});

describe("contentUrl", () => {
  it("encodes the path and asks for a download", () => {
    expect(contentUrl("docs/a b&c.txt", false, "/gui")).toBe("/gui/api/files/content?path=docs%2Fa+b%26c.txt");
    expect(contentUrl("čaj.pdf", true, "/gui")).toBe("/gui/api/files/content?path=%C4%8Daj.pdf&download=1");
  });
});

describe("decodeTextPreview", () => {
  const enc = new TextEncoder();
  it("a whole file is all of its text", () => {
    expect(decodeTextPreview(enc.encode("zdravo"), 6)).toEqual({ text: "zdravo", cut: false });
  });
  it("a character cut by the range is dropped", () => {
    const b = enc.encode("ćao"); // ć is two bytes
    expect(decodeTextPreview(b.slice(0, 1), 4)).toEqual({ text: "", cut: true });
    expect(decodeTextPreview(b.slice(0, 3), 4)).toEqual({ text: "ća", cut: true });
  });
});
