// Packages of `make release` that shfd serves (shfd serve --downloads, docs/install.md):
// GET /v1/downloads lists them, GET /downloads/<name> sends one. Both are shfd's own
// routes on the same origin as the GUI and need no token, so the Install page uses them
// before sign-in too.

export interface Package {
  name: string; // shared-folder-<version>-<os>-<arch>.tar.gz
  version: string;
  os: string; // GOOS
  arch: string; // GOARCH
  size: number;
  modified: string;
  sha256?: string; // its line in SHA256SUMS
  url: string; // /downloads/<name>
}

export interface Downloads {
  enabled: boolean; // shfd serve has --downloads
  packages: Package[]; // newest first
  sha256sums?: string; // /downloads/SHA256SUMS
}

export type MacArch = "arm64" | "amd64";

export async function getDownloads(fetchFn: typeof fetch = fetch): Promise<Downloads | null> {
  try {
    const r = await fetchFn("/v1/downloads", { credentials: "omit" });
    if (!r.ok) return null;
    const d = (await r.json()) as Downloads;
    return Array.isArray(d?.packages) ? d : null;
  } catch {
    return null; // shfd unreachable, or an older shfd that answers 404
  }
}

// pick is the newest package for a platform; the list comes newest first.
export function pick(d: Downloads | null | undefined, os: string, arch: string): Package | undefined {
  return d?.packages.find((p) => p.os === os && p.arch === arch);
}

// dirOf is the directory a package unpacks into (make release tars that one directory).
export function dirOf(p: Package): string {
  return p.name.replace(/\.tar\.gz$/, "");
}

export function formatSize(n: number): string {
  if (n >= 1 << 20) return `${(n / (1 << 20)).toFixed(1)} MB`;
  if (n >= 1 << 10) return `${Math.round(n / (1 << 10))} KB`;
  return `${n} B`;
}

type Nav = { userAgentData?: { getHighEntropyValues?: (h: string[]) => Promise<{ architecture?: string }> } };

// detectMacArch guesses Apple chip or Intel. Only Chromium tells (userAgentData); Safari and
// Firefox report "Intel" on every Mac, so the default is the Apple chip, what Macs sold since
// 2020 have. The page shows both and `uname -m` decides.
export async function detectMacArch(
  nav: Nav | undefined = typeof navigator === "undefined" ? undefined : (navigator as Nav),
): Promise<MacArch> {
  try {
    const v = await nav?.userAgentData?.getHighEntropyValues?.(["architecture"]);
    if (v?.architecture === "x86") return "amd64";
  } catch {
    // not allowed or not there: keep the default
  }
  return "arm64";
}
