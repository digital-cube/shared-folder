import type { Row } from "@jsudc/core";
import { afterEach, describe, expect, it } from "vitest";
import { isMenuKey, kindLabel, menuItems, placeMenu, stemRange } from "./filemenu";
import { setLang } from "./lang";

afterEach(() => setLang("sr"));

const row = (name: string, kind = "file") => ({ __meta: { id: name }, name, kind, path: name }) as unknown as Row;
const actions = (r: Row) => menuItems(r).map((i) => `${i.action}${i.disabled ? "(off)" : ""}`);

describe("menuItems", () => {
  it("a file: info, view, download, rename, remove", () => {
    expect(actions(row("hq720.jpg"))).toEqual(["info", "view", "download", "rename", "remove"]);
    expect(actions(row("film.mp4"))).toEqual(["info", "view", "download", "rename", "remove"]);
    expect(actions(row("pesma.mp3"))).toEqual(["info", "view", "download", "rename", "remove"]);
  });
  it("a file with no preview has Prikaži off, with the reason", () => {
    expect(actions(row("noi.html"))).toEqual(["info", "view(off)", "download", "rename", "remove"]);
    expect(menuItems(row("peca-gaga-mare")).find((i) => i.action === "view")?.hint).toMatch(/nema pregleda/);
  });
  it("a folder: open, info, remove; .. has no menu", () => {
    expect(actions(row("123", "dir"))).toEqual(["open", "info", "remove"]);
    expect(menuItems(row("..", "dir"))).toEqual([]);
  });
  it("a member who is not an admin only reads: no rename, no remove", () => {
    expect(menuItems(row("a.txt"), false).map((i) => i.action)).toEqual(["info", "view", "download"]);
    expect(menuItems(row("123", "dir"), false).map((i) => i.action)).toEqual(["open", "info"]);
  });
  it("remove is the dangerous one", () => {
    expect(menuItems(row("a.txt")).filter((i) => i.danger).map((i) => i.action)).toEqual(["remove"]);
  });
});

describe("kindLabel", () => {
  it("the kind in words with the extension", () => {
    expect(kindLabel(row("MZ9_7952.jpeg"))).toBe("Slika (JPEG)");
    expect(kindLabel(row("film.MP4"))).toBe("Video (MP4)");
    expect(kindLabel(row("ivo123"))).toBe("Fajl");
    expect(kindLabel(row("123", "dir"))).toBe("Folder");
  });
  it("in English", () => {
    setLang("en");
    expect(kindLabel(row("MZ9_7952.jpeg"))).toBe("Image (JPEG)");
    expect(kindLabel(row("ivo123"))).toBe("File");
    expect(menuItems(row("a.txt")).map((i) => i.label)).toEqual(["Info", "View", "Download", "Rename", "Delete"]);
  });
});

describe("stemRange", () => {
  it("selects the name without the extension", () => {
    expect(stemRange("hq720.jpg")).toEqual([0, 5]);
    expect(stemRange("arhiva.tar.gz")).toEqual([0, 10]);
    expect(stemRange("ivo123")).toEqual([0, 6]);
    expect(stemRange(".bashrc")).toEqual([0, 7]);
  });
});

describe("placeMenu", () => {
  it("stays at the pointer when it fits, flips when it would leave the screen", () => {
    expect(placeMenu(100, 100, 200, 180, 1280, 800)).toEqual({ left: 100, top: 100 });
    expect(placeMenu(1200, 100, 200, 180, 1280, 800)).toEqual({ left: 1000, top: 100 });
    expect(placeMenu(100, 700, 200, 180, 1280, 800)).toEqual({ left: 100, top: 520 });
    expect(placeMenu(50, 50, 200, 180, 120, 100)).toEqual({ left: 8, top: 8 });
  });
});

describe("isMenuKey", () => {
  it("the menu key and Shift+F10", () => {
    expect(isMenuKey({ key: "ContextMenu", shiftKey: false })).toBe(true);
    expect(isMenuKey({ key: "F10", shiftKey: true })).toBe(true);
    expect(isMenuKey({ key: "F10", shiftKey: false })).toBe(false);
    expect(isMenuKey({ key: "Enter", shiftKey: true })).toBe(false);
  });
});
