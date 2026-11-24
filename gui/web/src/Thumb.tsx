// A row's picture: the type icon at once, the thumbnail over it once it
// has loaded. A failed load leaves the icon; a thumbnail that becomes ready
// later (the list refreshes) loads then, with no reload of the page.

import type { Row } from "@jsudc/core";
import { useState, type ReactNode } from "react";
import { guiUrl } from "./api";
import { FileIcon } from "./FileIcon";
import { fileKind, pickVariant, type Variant } from "./filekind";

export function thumbUrl(hash: string, size: Variant, base?: string): string {
  return guiUrl(`/api/thumb/${hash}?size=${size}`, base);
}

const dpr = () => (typeof window === "undefined" ? 1 : window.devicePixelRatio || 1);

/**
 * `px` is the box in CSS pixels; it picks the size asked of shfd. The box
 * itself is sized by the caller's CSS (the `.thumb` class fills it).
 */
export function Thumb({ row, px, className }: { row: Row; px: number; className?: string }) {
  const name = String(row.name ?? "");
  const kind = fileKind(name, String(row.kind ?? ""));
  const hash = String(row.hash_full ?? "");
  const ready = row.thumb === "ready" && /^[0-9a-f]{64}$/.test(hash);
  // Keyed by the hash: another content starts again from its icon.
  return (
    <span className={`thumb ${className ?? ""}`} data-kind={kind}>
      <ThumbImage key={hash} want={ready ? thumbUrl(hash, pickVariant(px, dpr())) : null}>
        <FileIcon kind={kind} up={kind === "folder" && name === ".."} />
      </ThumbImage>
    </span>
  );
}

function ThumbImage({ want, children }: { want: string | null; children: ReactNode }) {
  // The last src that loaded (shown), and the one that failed (not asked again).
  const [shown, setShown] = useState<string | null>(null);
  const [failed, setFailed] = useState<string | null>(null);
  const visible = want ? shown : null;
  const loading = want && want !== shown && want !== failed ? want : null;
  return (
    <>
      {visible ? null : children}
      {visible ? <img className="thumb-img" src={visible} alt="" decoding="async" draggable={false} /> : null}
      {loading ? (
        // In the layout but transparent, so loading="lazy" still sees it; a
        // bigger size loading over a smaller one keeps the smaller in view.
        <img
          className="thumb-img loading"
          src={loading}
          alt=""
          loading="lazy"
          decoding="async"
          draggable={false}
          onLoad={() => setShown(loading)}
          onError={() => setFailed(loading)}
        />
      ) : null}
    </>
  );
}
