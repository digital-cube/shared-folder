// The picture of an account, or its initials on the account's colour
// (shared/avatar.ts). One component for the header, Members, Logs and
// Moj nalog.

import { useState } from "react";
import { avatarColor, avatarSrc, initials } from "../../shared/avatar";
import { BASE } from "./api";

export interface AvatarProps {
  username: string;
  displayName?: string | null;
  /** avatar_url as shfd gives it ("/v1/users/<u>/avatar?v=<n>"); null or absent: initials. */
  url?: string | null;
  /** A picture not yet saved (an object URL of the chosen file), shown instead of url. */
  preview?: string | null;
  /** Side in pixels; the CSS of .avatar gives the default. */
  size?: number;
  /** A revoked member: initials in grey. */
  muted?: boolean;
  base?: string;
  className?: string;
}

export function Avatar({ username, displayName, url, preview, size, muted, base = BASE, className }: AvatarProps) {
  const src = preview ?? avatarSrc(url, base);
  // A picture that does not load (removed in the meantime) falls back to
  // the initials; a new src tries again.
  const [failed, setFailed] = useState<string | null>(null);
  const dim = size ? { width: size, height: size, fontSize: Math.max(10, Math.round(size * 0.4)) } : {};
  const cls = `avatar ${className ?? ""}`.trim();
  if (src && failed !== src) {
    return (
      <span className={`${cls} has-img`} style={dim} data-testid="avatar">
        <img src={src} alt="" width={size} height={size} loading="lazy" decoding="async" draggable={false} onError={() => setFailed(src)} />
      </span>
    );
  }
  const colors = muted ? { background: "var(--g-bg-sunk)", color: "var(--g-faint)" } : { background: avatarColor(username), color: "#fff" };
  return (
    <span className={cls} style={{ ...dim, ...colors }} title={displayName || undefined} aria-hidden="true" data-testid="avatar">
      {initials(username, displayName)}
    </span>
  );
}
