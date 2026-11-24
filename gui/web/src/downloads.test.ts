import { describe, expect, it } from "vitest";
import { detectMacArch, dirOf, formatSize, getDownloads, pick, type Downloads, type Package } from "./downloads";

const pkg = (version: string, os: string, arch: string): Package => {
  const name = `shared-folder-${version}-${os}-${arch}.tar.gz`;
  return { name, version, os, arch, size: 1, modified: "", url: `/downloads/${name}` };
};

const list: Downloads = {
  enabled: true,
  packages: [pkg("v2", "darwin", "arm64"), pkg("v2", "linux", "amd64"), pkg("v1", "linux", "amd64"), pkg("v1", "darwin", "amd64")],
  sha256sums: "/downloads/SHA256SUMS",
};

describe("pick", () => {
  it("takes the newest package of the platform", () => {
    expect(pick(list, "linux", "amd64")?.version).toBe("v2");
    expect(pick(list, "darwin", "amd64")?.version).toBe("v1");
  });
  it("has none for a missing platform or list", () => {
    expect(pick(list, "linux", "arm64")).toBeUndefined();
    expect(pick(null, "linux", "amd64")).toBeUndefined();
    expect(pick(undefined, "linux", "amd64")).toBeUndefined();
  });
});

describe("dirOf / formatSize", () => {
  it("names the unpacked directory", () => {
    expect(dirOf(pkg("v0.2.0-3-gabc", "linux", "amd64"))).toBe("shared-folder-v0.2.0-3-gabc-linux-amd64");
  });
  it("formats sizes", () => {
    expect(formatSize(512)).toBe("512 B");
    expect(formatSize(4096)).toBe("4 KB");
    expect(formatSize(7.5 * 1024 * 1024)).toBe("7.5 MB");
  });
});

describe("getDownloads", () => {
  const answer = (status: number, body: unknown) =>
    (async () => new Response(JSON.stringify(body), { status })) as unknown as typeof fetch;
  it("returns the list", async () => {
    expect(await getDownloads(answer(200, list))).toEqual(list);
  });
  it("is null when shfd has no such route or does not answer", async () => {
    expect(await getDownloads(answer(404, { error: {} }))).toBeNull();
    expect(await getDownloads(answer(200, { nope: 1 }))).toBeNull();
    expect(await getDownloads((async () => Promise.reject(new Error("down"))) as unknown as typeof fetch)).toBeNull();
  });
});

describe("detectMacArch", () => {
  const nav = (architecture?: string) => ({
    userAgentData: { getHighEntropyValues: async () => ({ architecture }) },
  });
  it("is Intel only when Chromium says x86", async () => {
    expect(await detectMacArch(nav("x86"))).toBe("amd64");
    expect(await detectMacArch(nav("arm"))).toBe("arm64");
    expect(await detectMacArch({})).toBe("arm64");
    expect(await detectMacArch(undefined)).toBe("arm64");
  });
});
