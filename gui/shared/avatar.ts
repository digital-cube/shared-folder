// Profile pictures of accounts, the same on the server and in the browser:
// shfd keeps one 256×256 JPEG per account and gives its URL as avatar_url
// ("/v1/users/<username>/avatar?v=<version>", null without one); the GUI
// serves it at <base>/api/avatar/<username>?v=<version> with the session's
// credential. Without a picture the initials show on a colour that comes
// from the username, so the same account has the same colour everywhere.

/** The largest picture shfd takes (server.MaxAvatarBytes). */
export const AVATAR_MAX_BYTES = 5 * 1024 * 1024;

/** The types a picture may have; shfd checks the content itself again. */
export const AVATAR_TYPES = ["image/png", "image/jpeg", "image/webp", "image/gif"] as const;

/** What the file input offers. */
export const AVATAR_ACCEPT = AVATAR_TYPES.join(",");

/** Why a chosen file is not sent; null when it may be. */
export type AvatarFileProblem = "too_large" | "unsupported" | null;

/** What is wrong with a chosen file before it is sent, by its size and type. */
export function avatarFileProblem(file: { size: number; type: string }): AvatarFileProblem {
  if (file.type && !(AVATAR_TYPES as readonly string[]).includes(file.type.toLowerCase())) return "unsupported";
  if (file.size > AVATAR_MAX_BYTES) return "too_large";
  return null;
}

/**
 * The initials of an account: the first letters of the first two words of
 * its display name, or else of the parts of its username ("ana.petrovic"
 * → "AP"), or else its first letter.
 */
export function initials(username: string, displayName?: string | null): string {
  const words = (s: string) => s.split(/[\s._\-@]+/u).filter((w) => /\p{L}|\p{N}/u.test(w));
  const first = (w: string) => ([...w].find((c) => /\p{L}|\p{N}/u.test(c)) ?? "").toLocaleUpperCase();
  for (const src of [displayName ?? "", username]) {
    const ws = words(src.trim());
    if (ws.length >= 2) return first(ws[0]) + first(ws[1]);
    if (ws.length === 1) return first(ws[0]);
  }
  return "?";
}

/** A hue from the username (FNV-1a), the same everywhere it is shown. */
export function avatarHue(username: string): number {
  let h = 0x811c9dc5;
  for (const ch of username.toLowerCase()) {
    h ^= ch.codePointAt(0)!;
    h = Math.imul(h, 0x01000193) >>> 0;
  }
  return h % 360;
}

/** The background of the initials: dark enough for white letters in either theme. */
export function avatarColor(username: string): string {
  return `hsl(${avatarHue(username)} 55% 40%)`;
}

const SHFD_AVATAR = /^\/v1\/users\/([^/?#]+)\/avatar\?v=(\d+)$/;

/**
 * The GUI's URL of a picture whose avatar_url shfd gave, under base;
 * null without one (or for a URL that is not one of shfd's pictures).
 */
export function avatarSrc(avatarUrl: string | null | undefined, base: string): string | null {
  const m = avatarUrl ? SHFD_AVATAR.exec(avatarUrl) : null;
  return m ? `${base}/api/avatar/${m[1]}?v=${m[2]}` : null;
}
