export type Os = "linux" | "macos" | "windows";

type Nav = { platform?: string; userAgent?: string; userAgentData?: { platform?: string } };

// detectOs picks the install guide's OS tab for the visitor's browser: macOS and Windows are
// recognised, anything else (Linux, ChromeOS, unknown) falls back to Linux.
export function detectOs(nav: Nav | undefined = typeof navigator === "undefined" ? undefined : navigator): Os {
  if (!nav) return "linux";
  const s = [nav.userAgentData?.platform, nav.platform, nav.userAgent].filter(Boolean).join(" ").toLowerCase();
  if (/mac|darwin/.test(s)) return "macos";
  if (/\bwin/.test(s)) return "windows"; // \b: "darwin" must not count as Windows
  return "linux";
}
