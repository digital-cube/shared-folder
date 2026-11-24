// The preview of one file of the Files page: a click on a file's name (the
// table) or tile (Mreža) opens it. An image large (the 256 px thumbnail at
// once when there is one, the original over it once loaded), the first
// 200 KB of a text, a PDF as buttons, anything else as its data and
// Preuzmi. Everything comes through <base>/api/files/content, which only
// serves inline what shfd's whitelist allows (preview.ts mirrors it).
//
// jsudc's own folder preview (folder_options.preview) reads a filesystem
// source's /udc/files/blob, which this collection has not, so the view
// keeps it off and the cells open this one through PreviewContext.

import type { Row } from "@jsudc/core";
import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { bytes, stamp } from "./format";
import { Download, ExternalLink, X } from "./icons";
import { contentUrl, decodeTextPreview, previewKind, TEXT_PREVIEW_BYTES } from "./preview";
import { Thumb, thumbUrl } from "./Thumb";

const PreviewContext = createContext<((row: Row) => void) | null>(null);

/** Opens the preview of a file row; null outside the Files page. */
export function usePreview(): ((row: Row) => void) | null {
  return useContext(PreviewContext);
}

/** Gives the cells under it a way to open the preview, and shows it. */
export function PreviewProvider({ children }: { children: ReactNode }) {
  const [row, setRow] = useState<Row | null>(null);
  return (
    <PreviewContext.Provider value={setRow}>
      {children}
      {row ? <FilePreview row={row} onClose={() => setRow(null)} /> : null}
    </PreviewContext.Provider>
  );
}

const str = (v: unknown) => (v === null || v === undefined ? "" : String(v));

export function FilePreview({ row, onClose }: { row: Row; onClose: () => void }) {
  const name = str(row.name);
  const path = str(row.path);
  const kind = previewKind(name, str(row.kind));
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal preview" role="dialog" aria-label={name} data-testid="file-preview">
        <div className="preview-head">
          <h3 title={path}>{name}</h3>
          <span className="spacer" />
          {kind !== "none" ? (
            <a className="btn sm" href={contentUrl(path)} target="_blank" rel="noopener noreferrer">
              <ExternalLink size={14} /> Otvori
            </a>
          ) : null}
          <a className="btn sm primary" href={contentUrl(path, true)} download={name} data-testid="preview-download">
            <Download size={14} /> Preuzmi
          </a>
          <button className="btn ghost sm" onClick={onClose} aria-label="Zatvori" autoFocus>
            <X size={14} />
          </button>
        </div>
        <div className="preview-body">
          {kind === "image" ? <ImagePreview row={row} path={path} name={name} /> : null}
          {kind === "text" ? <TextPreview path={path} size={Number(row.size ?? 0)} /> : null}
          {kind === "pdf" ? (
            <p className="preview-note">
              PDF se otvara u novom tabu (<b>Otvori</b>) ili preuzima (<b>Preuzmi</b>); u ovom prozoru se ne prikazuje.
            </p>
          ) : null}
          {kind === "none" ? (
            <div className="preview-none">
              <Thumb row={row} px={96} />
              <p className="preview-note">Za ovaj tip fajla nema pregleda; preuzmi ga.</p>
            </div>
          ) : null}
        </div>
        <dl className="preview-meta">
          <dt>Putanja</dt>
          <dd className="mono">{path}</dd>
          <dt>Veličina</dt>
          <dd>{bytes(row.size as number | null)}</dd>
          <dt>Izmenjen</dt>
          <dd>{row.mtime ? stamp(str(row.mtime)) : "—"}</dd>
          <dt>Poslao</dt>
          <dd>{str(row.updated_by) || "—"}</dd>
          <dt>Hash</dt>
          <dd className="mono" style={{ wordBreak: "break-all" }}>
            {str(row.hash_full) || str(row.hash)}
          </dd>
        </dl>
      </div>
    </div>
  );
}

/** The 256 px thumbnail at once (when shfd has one), the original over it. */
function ImagePreview({ row, path, name }: { row: Row; path: string; name: string }) {
  const hash = str(row.hash_full);
  const quick = row.thumb === "ready" && /^[0-9a-f]{64}$/.test(hash) ? thumbUrl(hash, "m") : null;
  const [loaded, setLoaded] = useState(false);
  const [failed, setFailed] = useState(false);
  return (
    <div className="preview-image">
      {quick && !loaded ? <img src={quick} alt="" className="quick" /> : null}
      {failed ? (
        <p className="preview-note err-text">Slika nije mogla da se učita.</p>
      ) : (
        <img
          src={contentUrl(path)}
          alt={name}
          className={loaded ? "" : "loading"}
          onLoad={() => setLoaded(true)}
          onError={() => setFailed(true)}
          data-testid="preview-image"
        />
      )}
    </div>
  );
}

type TextState = { state: "loading" } | { state: "ok"; text: string; cut: boolean } | { state: "error"; message: string };

/** The first TEXT_PREVIEW_BYTES of a text file, asked for with Range. */
function TextPreview({ path, size }: { path: string; size: number }) {
  const [s, setS] = useState<TextState>({ state: "loading" });
  useEffect(() => {
    const ctl = new AbortController();
    (async () => {
      const res = await fetch(contentUrl(path), {
        credentials: "same-origin",
        headers: { range: `bytes=0-${TEXT_PREVIEW_BYTES - 1}` },
        signal: ctl.signal,
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        setS({ state: "error", message: body.error ?? `Greška ${res.status}` });
        return;
      }
      // A 200 (a server that ignores Range) is read only as far as needed.
      const buf = await readUpTo(res, TEXT_PREVIEW_BYTES);
      const total = Number(/\/(\d+)$/.exec(res.headers.get("content-range") ?? "")?.[1] ?? size);
      setS({ state: "ok", ...decodeTextPreview(buf, Math.max(total, size)) });
    })().catch((err) => {
      if (!ctl.signal.aborted) setS({ state: "error", message: (err as Error).message });
    });
    return () => ctl.abort();
  }, [path, size]);
  if (s.state === "loading") return <p className="preview-note">Učitavam…</p>;
  if (s.state === "error") return <p className="preview-note err-text">{s.message}</p>;
  return (
    <>
      <pre className="preview-text" data-testid="preview-text">
        {s.text}
      </pre>
      {s.cut ? (
        <p className="preview-note">
          Prikazano je prvih {bytes(TEXT_PREVIEW_BYTES)} od {bytes(size)}; ceo fajl preuzmi.
        </p>
      ) : null}
    </>
  );
}

async function readUpTo(res: Response, limit: number): Promise<Uint8Array> {
  if (!res.body) return new Uint8Array(await res.arrayBuffer()).slice(0, limit);
  const reader = res.body.getReader();
  const parts: Uint8Array[] = [];
  let n = 0;
  while (n < limit) {
    const { value, done } = await reader.read();
    if (done) break;
    parts.push(value);
    n += value.length;
  }
  await reader.cancel().catch(() => undefined);
  const out = new Uint8Array(Math.min(n, limit));
  let at = 0;
  for (const p of parts) {
    const take = Math.min(p.length, out.length - at);
    out.set(p.subarray(0, take), at);
    at += take;
    if (at >= out.length) break;
  }
  return out;
}
