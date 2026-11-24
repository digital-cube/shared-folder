// The preview of one file of the Files page: a click on a file's name (the
// table) or tile (Mreža), or Prikaži in its context menu, opens it. An
// image large (the 256 px thumbnail at once when there is one, the
// original over it once loaded), the first 200 KB of a text, a video or a
// sound in the browser's own player, a PDF as buttons, anything else as
// its data and Preuzmi. Everything comes through <base>/api/files/content,
// which only serves inline what shfd's whitelist allows (preview.ts
// mirrors it).
//
// jsudc's own folder preview (folder_options.preview) reads a filesystem
// source's /udc/files/blob, which this collection has not, so the view
// keeps it off and the cells open this one through PreviewContext, which
// FileActionsProvider (FileMenu.tsx) gives them.

import type { Row } from "@jsudc/core";
import { createContext, useContext, useEffect, useState } from "react";
import { kindLabel } from "./filemenu";
import { bytes, stamp } from "./format";
import { useTr } from "./i18n";
import { Download, ExternalLink, X } from "./icons";
import { getLang, intlLocale, LANG_HEADER } from "./lang";
import { contentUrl, decodeTextPreview, previewKind, TEXT_PREVIEW_BYTES } from "./preview";
import { Thumb, thumbUrl } from "./Thumb";

export const PreviewContext = createContext<((row: Row) => void) | null>(null);

/** Opens the preview of a file row; null outside the Files page. */
export function usePreview(): ((row: Row) => void) | null {
  return useContext(PreviewContext);
}

const str = (v: unknown) => (v === null || v === undefined ? "" : String(v));

export function FilePreview({ row, onClose }: { row: Row; onClose: () => void }) {
  const name = str(row.name);
  const path = str(row.path);
  const kind = previewKind(name, str(row.kind));
  const tr = useTr();
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
              <ExternalLink size={14} /> {tr("Otvori", "Open")}
            </a>
          ) : null}
          <a className="btn sm primary" href={contentUrl(path, true)} download={name} data-testid="preview-download">
            <Download size={14} /> {tr("Preuzmi", "Download")}
          </a>
          <button className="btn ghost sm" onClick={onClose} aria-label={tr("Zatvori", "Close")} autoFocus>
            <X size={14} />
          </button>
        </div>
        <div className="preview-body">
          {kind === "image" ? <ImagePreview row={row} path={path} name={name} /> : null}
          {kind === "text" ? <TextPreview path={path} size={Number(row.size ?? 0)} /> : null}
          {kind === "video" ? <MediaPreview path={path} video /> : null}
          {kind === "audio" ? (
            <div className="preview-audio">
              <Thumb row={row} px={72} />
              <MediaPreview path={path} />
            </div>
          ) : null}
          {kind === "pdf" ? (
            <p className="preview-note">
              {tr("PDF se otvara u novom tabu", "A PDF opens in a new tab")} (<b>{tr("Otvori", "Open")}</b>){" "}
              {tr("ili preuzima", "or downloads")} (<b>{tr("Preuzmi", "Download")}</b>);{" "}
              {tr("u ovom prozoru se ne prikazuje.", "it is not shown in this window.")}
            </p>
          ) : null}
          {kind === "none" ? (
            <div className="preview-none">
              <Thumb row={row} px={96} />
              <p className="preview-note">{tr("Za ovaj tip fajla nema pregleda; preuzmi ga.", "There is no preview for this type of file; download it.")}</p>
            </div>
          ) : null}
        </div>
        <FileMeta row={row} />
      </div>
    </div>
  );
}

/** The data of a file (or folder): the preview's foot and the info dialog. */
export function FileMeta({ row, full = false }: { row: Row; full?: boolean }) {
  const dir = row.kind === "dir";
  const size = row.size as number | null;
  const tr = useTr();
  return (
    <dl className="preview-meta" data-testid="file-meta">
      {full ? (
        <>
          <dt>{tr("Ime", "Name")}</dt>
          <dd style={{ wordBreak: "break-all" }}>{str(row.name)}</dd>
          <dt>{tr("Tip", "Type")}</dt>
          <dd>{kindLabel(row)}</dd>
        </>
      ) : null}
      <dt>{tr("Putanja", "Path")}</dt>
      <dd className="mono" style={{ wordBreak: "break-all" }}>
        {str(row.path)}
      </dd>
      {dir ? null : (
        <>
          <dt>{tr("Veličina", "Size")}</dt>
          <dd>
            {bytes(size)}
            {full && typeof size === "number" && size >= 1024 ? <span className="faint"> ({size.toLocaleString(intlLocale())} B)</span> : null}
          </dd>
          <dt>{tr("Izmenjen", "Modified")}</dt>
          <dd>{row.mtime ? stamp(str(row.mtime)) : "—"}</dd>
        </>
      )}
      <dt>{dir ? tr("Napravio", "Created by") : tr("Poslao", "Uploaded by")}</dt>
      <dd>{str(row.updated_by) || "—"}</dd>
      {full && row.seq !== null && row.seq !== undefined ? (
        <>
          <dt>Seq</dt>
          <dd className="mono">{str(row.seq)}</dd>
        </>
      ) : null}
      {dir ? null : (
        <>
          <dt>Hash</dt>
          <dd className="mono" style={{ wordBreak: "break-all" }}>
            {str(row.hash_full) || str(row.hash)}
          </dd>
        </>
      )}
    </dl>
  );
}

/**
 * A video or a sound in the browser's player. It streams through the GUI
 * (Range, so seeking works); a format the browser cannot play says so.
 */
function MediaPreview({ path, video = false }: { path: string; video?: boolean }) {
  const [failed, setFailed] = useState(false);
  const tr = useTr();
  if (failed) {
    return (
      <p className="preview-note err-text">
        {video
          ? tr("Browser ne može da pusti ovaj video; preuzmi ga.", "The browser cannot play this video; download it.")
          : tr("Browser ne može da pusti ovaj zvuk; preuzmi ga.", "The browser cannot play this audio; download it.")}
      </p>
    );
  }
  const props = { src: contentUrl(path), controls: true, preload: "metadata", onError: () => setFailed(true) } as const;
  return video ? (
    <div className="preview-video">
      <video {...props} playsInline data-testid="preview-video" />
    </div>
  ) : (
    <audio {...props} data-testid="preview-audio" />
  );
}

/** The 256 px thumbnail at once (when shfd has one), the original over it. */
function ImagePreview({ row, path, name }: { row: Row; path: string; name: string }) {
  const hash = str(row.hash_full);
  const quick = row.thumb === "ready" && /^[0-9a-f]{64}$/.test(hash) ? thumbUrl(hash, "m") : null;
  const [loaded, setLoaded] = useState(false);
  const [failed, setFailed] = useState(false);
  const tr = useTr();
  return (
    <div className="preview-image">
      {quick && !loaded ? <img src={quick} alt="" className="quick" /> : null}
      {failed ? (
        <p className="preview-note err-text">{tr("Slika nije mogla da se učita.", "The image could not be loaded.")}</p>
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
  const tr = useTr();
  useEffect(() => {
    const ctl = new AbortController();
    (async () => {
      const res = await fetch(contentUrl(path), {
        credentials: "same-origin",
        headers: { range: `bytes=0-${TEXT_PREVIEW_BYTES - 1}`, [LANG_HEADER]: getLang() },
        signal: ctl.signal,
      });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        setS({ state: "error", message: body.error ?? `${getLang() === "en" ? "Error" : "Greška"} ${res.status}` });
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
  if (s.state === "loading") return <p className="preview-note">{tr("Učitavam…", "Loading…")}</p>;
  if (s.state === "error") return <p className="preview-note err-text">{s.message}</p>;
  return (
    <>
      <pre className="preview-text" data-testid="preview-text">
        {s.text}
      </pre>
      {s.cut ? (
        <p className="preview-note">
          {tr("Prikazano je prvih", "Showing the first")} {bytes(TEXT_PREVIEW_BYTES)} {tr("od", "of")} {bytes(size)};{" "}
          {tr("ceo fajl preuzmi.", "download the whole file.")}
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
