// The type icon of a file: our own drawing, a sheet of paper with a folded
// corner and a coloured tile with a letter (W, X, P, PDF) or a glyph. Not
// any vendor's logo: the letter in a colour people know is enough. One
// 48×48 viewBox, drawn to the size of its box, legible from 16 to 256 px.

import type { FileKind } from "./filekind";

interface Tile {
  color: string;
  /** The letters on the tile; a glyph when absent. */
  text?: string;
  glyph?: "image" | "audio" | "video";
}

const TILES: Partial<Record<FileKind, Tile>> = {
  word: { color: "#2563eb", text: "W" },
  excel: { color: "#15803d", text: "X" },
  powerpoint: { color: "#ea580c", text: "P" },
  pdf: { color: "#dc2626", text: "PDF" },
  code: { color: "#475569", text: "</>" },
  archive: { color: "#a16207", text: "ZIP" },
  image: { color: "#0d9488", glyph: "image" },
  audio: { color: "#db2777", glyph: "audio" },
  video: { color: "#7c3aed", glyph: "video" },
};

const PAPER = "#ffffff";
const EDGE = "#c3c9d3";
const FOLD = "#e6e9ef";
const LINE = "#cdd3dc";

/** The label a screen reader gets for a kind. */
export const KIND_LABEL: Record<FileKind, string> = {
  folder: "Folder",
  image: "Slika",
  pdf: "PDF",
  word: "Word dokument",
  excel: "Excel tabela",
  powerpoint: "PowerPoint prezentacija",
  text: "Tekst",
  code: "Kod",
  archive: "Arhiva",
  audio: "Zvuk",
  video: "Video",
  other: "Fajl",
};

export function FileIcon({ kind, up = false, className }: { kind: FileKind; up?: boolean; className?: string }) {
  return (
    <svg
      viewBox="0 0 48 48"
      className={className ?? "file-icon"}
      data-kind={kind}
      aria-hidden
      focusable="false"
      shapeRendering="geometricPrecision"
    >
      {kind === "folder" ? <FolderShape up={up} /> : <Sheet kind={kind} />}
    </svg>
  );
}

function FolderShape({ up }: { up: boolean }) {
  return (
    <g>
      <path d="M4 12.5a3 3 0 0 1 3-3h11.2a3 3 0 0 1 2.2 1l2.6 2.9H41a3 3 0 0 1 3 3V38a3 3 0 0 1-3 3H7a3 3 0 0 1-3-3z" fill="#4f8fe0" />
      <path d="M4 18.5a3 3 0 0 1 3-3h34a3 3 0 0 1 3 3V38a3 3 0 0 1-3 3H7a3 3 0 0 1-3-3z" fill="#74aaf0" />
      {up ? (
        <path d="M24 22.5l-6.5 6.5h4.3v6.5h4.4V29h4.3z" fill="#ffffff" />
      ) : null}
    </g>
  );
}

function Sheet({ kind }: { kind: FileKind }) {
  const tile = TILES[kind];
  return (
    <g>
      {/* the sheet, its folded corner up right */}
      <path d="M12 3.75h17.5l10.25 10.25V42.25a2 2 0 0 1-2 2H12a2 2 0 0 1-2-2V5.75a2 2 0 0 1 2-2z" fill={PAPER} stroke={EDGE} strokeWidth="1.5" strokeLinejoin="round" />
      <path d="M29.5 3.75V12a2 2 0 0 0 2 2h8.25" fill={FOLD} stroke={EDGE} strokeWidth="1.5" strokeLinejoin="round" />
      {kind === "text" || kind === "other" ? (
        <g stroke={LINE} strokeWidth="2.25" strokeLinecap="round">
          {(kind === "text" ? [19, 24.5, 30, 35.5] : [24.5, 30]).map((y, i) => (
            <line key={y} x1="15.5" x2={i % 2 ? 29 : 34} y1={y} y2={y} />
          ))}
        </g>
      ) : null}
      {tile ? <TileShape tile={tile} /> : null}
    </g>
  );
}

function TileShape({ tile }: { tile: Tile }) {
  const long = (tile.text?.length ?? 1) > 1;
  return (
    <g>
      <rect x="3.5" y="23" width={long ? 31 : 23} height="17" rx="3.25" fill={tile.color} />
      {tile.text ? (
        <text
          x={long ? 19 : 15}
          y="31.75"
          fill="#ffffff"
          fontFamily="Inter, ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, Arial, sans-serif"
          fontWeight="800"
          fontSize={long ? 10.5 : 13.5}
          textAnchor="middle"
          dominantBaseline="central"
          letterSpacing={long ? "-0.2" : "0"}
        >
          {tile.text}
        </text>
      ) : tile.glyph === "image" ? (
        <g fill="#ffffff">
          <circle cx="10.5" cy="28" r="2.1" />
          <path d="M6.5 37.25l5.25-5.5 3 3 4.25-5 4.75 7.5z" />
        </g>
      ) : tile.glyph === "audio" ? (
        <path d="M17.5 26v8.2a2.6 2.6 0 1 1-1.6-2.4v-4.4l-5.4 1.4v6.1a2.6 2.6 0 1 1-1.6-2.4V27.3z" fill="#ffffff" />
      ) : (
        <path d="M12.5 26.75v9.5l7.75-4.75z" fill="#ffffff" />
      )}
    </g>
  );
}
