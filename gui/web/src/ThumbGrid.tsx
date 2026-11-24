// The "Mreža" tab of the Files page: a grid of tiles, each a square with
// the thumbnail or the type icon, the name under it, then the size and the
// date. The same rows as the table (search, the extension chip, paging),
// the same folder navigation (`props.folder`), tiles as large as the
// toolbar's slider says.
//
// jsudc's own "cards" renderer does not fit: its three sizes come from the
// server's view config, and a row without an image gets initials, not the
// type icon.

import type { Row } from "@jsudc/core";
import {
  EmptyState,
  ErrorState,
  isFolderRow,
  useI18n,
  type FolderNav,
  type RendererDefinition,
  type RendererProps,
} from "@jsudc/react";
import { Pager, tableRenderer } from "@jsudc/react/table";
import { LayoutGrid } from "lucide-react";
import { useEffect, useRef, type CSSProperties, type KeyboardEvent } from "react";
import { useAppMaybe } from "./App";
import { bytes } from "./format";
import { gridOrder, usePendingThumbs } from "./liveThumbs";
import { Thumb } from "./Thumb";
import {
  setThumbSize,
  THUMB_COMPACT_BELOW,
  THUMB_DEFAULT,
  THUMB_MAX,
  THUMB_MIN,
  THUMB_STEP,
  useThumbSize,
} from "./thumbsize";

const dateFmt = new Intl.DateTimeFormat("sr-Latn-RS", { day: "numeric", month: "numeric", year: "numeric" });

function shortDate(ts: unknown): string {
  if (!ts) return "";
  const d = new Date(String(ts));
  return Number.isNaN(d.getTime()) ? "" : dateFmt.format(d);
}

// A folder opened from the keyboard hands the focus to the first tile of
// the new listing, so Enter, Enter, … walks down a tree without the mouse.
let focusAfterOpen = false;

/** The live list polls for pending thumbnails, paused with the rest of "uživo". */
function useLive(props: RendererProps) {
  const app = useAppMaybe();
  usePendingThumbs(props.data, props.resetKey, { enabled: !app?.paused });
}

function ThumbGrid(props: RendererProps) {
  const { data, folder, mode, collection } = props;
  const size = useThumbSize();
  const { t, intlLocale } = useI18n();
  const scrollRef = useRef<HTMLDivElement>(null);
  const sentinelRef = useRef<HTMLDivElement>(null);
  useLive(props);

  useEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = 0;
  }, [props.resetKey]);

  const { hasNextPage, isFetchingNextPage, fetchNextPage } = data;
  useEffect(() => {
    const el = sentinelRef.current;
    const root = scrollRef.current;
    if (mode !== "infinite" || !el || !root) return;
    const io = new IntersectionObserver(
      (es) => {
        if (es.some((e) => e.isIntersecting) && hasNextPage && !isFetchingNextPage) fetchNextPage();
      },
      { root, rootMargin: "0px 0px 240px 0px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [mode, hasNextPage, isFetchingNextPage, fetchNextPage]);

  // The old listing stays on screen while the new one loads: arm on the
  // folder change, focus once the new rows are in.
  const focusArmed = useRef(false);
  useEffect(() => {
    if (focusAfterOpen) focusArmed.current = true;
    focusAfterOpen = false;
  }, [props.resetKey]);
  useEffect(() => {
    if (!focusArmed.current || data.isFetching || data.rows.length === 0) return;
    focusArmed.current = false;
    scrollRef.current?.querySelector<HTMLElement>(".tile")?.focus();
  }, [data.rows, data.isFetching]);

  const isDir = (r: Row) => (folder ? isFolderRow(r, folder) : r.kind === "dir");
  const rows = gridOrder(data.rows, isDir);
  const loading = data.isLoading && data.rows.length === 0;
  const showError = data.error !== null && data.rows.length === 0;
  const showEmpty = !loading && !showError && data.rows.length === 0;
  const compact = size < THUMB_COMPACT_BELOW;
  const style = { "--thumb": `${size}px` } as CSSProperties;

  return (
    <div className="tgrid-frame">
      <div className="tgrid-scroll" ref={scrollRef} data-testid="thumb-grid">
        {data.isFetching && !data.isLoading && !isFetchingNextPage ? <div className="tgrid-busy" aria-hidden /> : null}
        {loading ? (
          <div className="tgrid" style={style} aria-busy="true">
            {Array.from({ length: 12 }, (_, i) => (
              <div key={i} className="tile skeleton" aria-hidden>
                <span className="thumb" />
                <span className="tile-name">&nbsp;</span>
              </div>
            ))}
          </div>
        ) : null}
        {showError && data.error ? <ErrorState error={data.error} onRetry={data.refetch} /> : null}
        {showEmpty ? <EmptyState collection={collection} filtered={props.filtered} onClear={props.onClearFilters} /> : null}
        {rows.length > 0 ? (
          <div className="tgrid" style={style} data-compact={compact || undefined} role="list">
            {rows.map((row) => (
              <Tile key={row.__meta.id} row={row} px={size} dir={isDir(row)} folder={folder} compact={compact} />
            ))}
          </div>
        ) : null}
        {mode === "infinite" && data.rows.length > 0 ? (
          <div className="tgrid-foot" data-testid="infinite-footer">
            {isFetchingNextPage
              ? t("APP", "LOADING_MORE")
              : hasNextPage
                ? null
                : t("APP", "ALL_LOADED", { total: new Intl.NumberFormat(intlLocale).format(data.total ?? data.rows.length) })}
          </div>
        ) : null}
        <div ref={sentinelRef} className="tgrid-sentinel" />
      </div>
      {mode === "pages" && !showError && (data.total === null ? data.rows.length > 0 || props.page > 1 : data.total > 0) ? (
        <Pager page={props.page} limit={props.limit} total={data.total} shown={data.rows.length} onPage={props.onPage} />
      ) : null}
    </div>
  );
}

function Tile({ row, px, dir, folder, compact }: { row: Row; px: number; dir: boolean; folder?: FolderNav; compact: boolean }) {
  const name = String(row.name ?? "");
  const up = dir && name === "..";
  const path = folder ? String(row[folder.options.path_field] ?? "") : String(row.path ?? "");
  const open = dir && folder ? () => folder.open(path) : undefined;
  const onKeyDown = open
    ? (e: KeyboardEvent) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          focusAfterOpen = true;
          open();
        }
      }
    : undefined;
  const meta = up ? "Nazad" : dir ? "Folder" : [bytes(row.size as number | null), shortDate(row.mtime)].filter(Boolean).join(" · ");
  return (
    <div
      className={`tile${dir ? " dir" : ""}`}
      role="listitem"
      tabIndex={0}
      title={up ? "Gornji folder" : name}
      aria-label={up ? "Gornji folder" : dir ? `Folder ${name}` : name}
      data-testid={dir ? "tile-folder" : "tile-file"}
      data-path={path}
      onClick={open}
      onKeyDown={onKeyDown}
    >
      <Thumb row={row} px={px} />
      <span className="tile-name">{name}</span>
      {compact ? null : <span className="tile-meta">{meta}</span>}
    </div>
  );
}

/** The slider in the toolbar while the Grid is open. */
function ThumbSizeTool() {
  const size = useThumbSize();
  return (
    <label className="thumb-size" title="Veličina sličica">
      <span>Veličina sličica</span>
      <input
        type="range"
        min={THUMB_MIN}
        max={THUMB_MAX}
        step={THUMB_STEP}
        value={size}
        aria-valuetext={`${size} px`}
        onChange={(e) => setThumbSize(Number(e.target.value))}
        onDoubleClick={() => setThumbSize(THUMB_DEFAULT)}
        data-testid="thumb-size"
      />
    </label>
  );
}

export const thumbsRenderer: RendererDefinition = {
  name: "thumbs",
  component: ThumbGrid,
  icon: LayoutGrid,
  labelKey: "TAB_THUMBS",
  paging: true,
  editMode: false,
  toolbar: ThumbSizeTool,
};

/** jsudc's table, plus the refresh while thumbnails are pending. */
const Table = tableRenderer.component;
function LiveTable(props: RendererProps) {
  useLive(props);
  return <Table {...props} />;
}
export const filesTableRenderer: RendererDefinition = { ...tableRenderer, component: LiveTable };
