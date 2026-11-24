import { describe, expect, it } from "vitest";
import { fileKind, pickVariant } from "./filekind";

describe("fileKind", () => {
  it("names every group", () => {
    const cases: [string, string][] = [
      ["slika.jpg", "image"],
      ["slika.jpeg", "image"],
      ["ekran.png", "image"],
      ["telefon.heic", "image"],
      ["logo.svg", "image"],
      ["ugovor.pdf", "pdf"],
      ["pismo.doc", "word"],
      ["pismo.docx", "word"],
      ["pismo.odt", "word"],
      ["pismo.rtf", "word"],
      ["budzet.xls", "excel"],
      ["budzet.xlsx", "excel"],
      ["budzet.ods", "excel"],
      ["izvoz.csv", "excel"],
      ["prezentacija.ppt", "powerpoint"],
      ["prezentacija.pptx", "powerpoint"],
      ["prezentacija.odp", "powerpoint"],
      ["beleske.txt", "text"],
      ["README.md", "text"],
      ["server.log", "text"],
      ["index.html", "code"],
      ["stil.css", "code"],
      ["app.js", "code"],
      ["app.ts", "code"],
      ["podaci.json", "code"],
      ["feed.xml", "code"],
      ["ci.yml", "code"],
      ["ci.yaml", "code"],
      ["main.go", "code"],
      ["skripta.py", "code"],
      ["deploy.sh", "code"],
      ["schema.sql", "code"],
      ["arhiva.zip", "archive"],
      ["arhiva.tar", "archive"],
      ["arhiva.tar.gz", "archive"],
      ["arhiva.tgz", "archive"],
      ["arhiva.7z", "archive"],
      ["arhiva.rar", "archive"],
      ["pesma.mp3", "audio"],
      ["snimak.wav", "audio"],
      ["film.mp4", "video"],
      ["film.mov", "video"],
      ["nesto.bin", "other"],
    ];
    for (const [name, kind] of cases) expect(fileKind(name, "file"), name).toBe(kind);
  });

  it("a folder is a folder whatever its name", () => {
    expect(fileKind("docs", "dir")).toBe("folder");
    expect(fileKind("slike.jpg", "dir")).toBe("folder");
    expect(fileKind("..", "dir")).toBe("folder");
  });

  it("ignores the case of the extension", () => {
    expect(fileKind("IMG_0001.JPG", "file")).toBe("image");
    expect(fileKind("Ugovor.PDF", "file")).toBe("pdf");
    expect(fileKind("Plan.DocX", "file")).toBe("word");
    expect(fileKind("TABELA.XLSX", "file")).toBe("excel");
  });

  it("a name without an extension, a dot file or a trailing dot is other", () => {
    expect(fileKind("Makefile", "file")).toBe("other");
    expect(fileKind("ivo123", "file")).toBe("other");
    expect(fileKind(".bashrc", "file")).toBe("other");
    expect(fileKind(".pdf", "file")).toBe("other");
    expect(fileKind("cudno.", "file")).toBe("other");
    expect(fileKind("peca-gaga-mare~", "file")).toBe("other");
  });
});

describe("pickVariant", () => {
  it("the smallest variant that covers the box at dpr 1", () => {
    expect(pickVariant(28, 1)).toBe("s");
    expect(pickVariant(96, 1)).toBe("s");
    expect(pickVariant(97, 1)).toBe("m");
    expect(pickVariant(256, 1)).toBe("m");
    expect(pickVariant(257, 1)).toBe("l");
    expect(pickVariant(512, 1)).toBe("l");
    expect(pickVariant(900, 1)).toBe("l");
  });

  it("counts device pixels at dpr 2", () => {
    expect(pickVariant(48, 2)).toBe("s");
    expect(pickVariant(96, 2)).toBe("m");
    expect(pickVariant(97, 2)).toBe("m");
    expect(pickVariant(128, 2)).toBe("m");
    expect(pickVariant(129, 2)).toBe("l");
    expect(pickVariant(256, 2)).toBe("l");
    expect(pickVariant(257, 2)).toBe("l");
  });

  it("a missing or broken dpr counts as 1", () => {
    expect(pickVariant(97)).toBe("m");
    expect(pickVariant(97, 0)).toBe("m");
    expect(pickVariant(97, Number.NaN)).toBe("m");
  });
});
