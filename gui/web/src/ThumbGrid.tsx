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
import type { RowSelection } from "@jsudc/react";
import { useEffect, useMemo, useRef, type CSSProperties, type KeyboardEvent, type MouseEvent } from "react";
import { rangeIds, selectableOnly, UP_ID } from "./bulk";
import type React from "react";
import { useAppMaybe } from "./App";
import { bytes } from "./format";
import { useTr } from "./i18n";
import { intlLocale } from "./lang";
import { gridOrder, usePendingThumbs } from "./liveThumbs";
import { usePreview } from "./FilePreview";
import { useFileMenu, type OpenMenu } from "./FileMenu";
import { isMenuKey } from "./filemenu";
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

const dateFmts = new Map<string, Intl.DateTimeFormat>();

function shortDate(ts: unknown): string {
  if (!ts) return "";
  const d = new Date(String(ts));
  if (Number.isNaN(d.getTime())) return "";
  const loc = intlLocale();
  let f = dateFmts.get(loc);
  if (!f) dateFmts.set(loc, (f = new Intl.DateTimeFormat(loc, { day: "numeric", month: "numeric", year: "numeric" })));
  return f.format(d);
}

// A folder opened from the keyboard hands the focus to the first tile of
// the new listing, so Enter, Enter, … walks down a tree without the mouse.
let focusAfterOpen = false;

/**
 * The view's selection without the ".." row (bulk.ts), for the table and
 * the Grid alike; undefined while the view does not select (a member who
 * is not an admin).
 */
function useSelection(props: RendererProps): RowSelection | undefined {
  const { selection, data } = props;
  return useMemo(() => (selection ? selectableOnly(selection, data.rows) : undefined), [selection, data.rows]);
}

/** Is a dialog or the context menu open? Then Esc is theirs, not the selection's. */
const overlayOpen = () => document.querySelector(".modal-back, .ctx-menu, [role='dialog']") !== null;

/** The live list polls for pending thumbnails, paused with the rest of "uživo". */
function useLive(props: RendererProps) {
  const app = useAppMaybe();
  usePendingThumbs(props.data, props.resetKey, { enabled: !app?.paused });
}

function ThumbGrid(props: RendererProps) {
  const { data, folder, mode, collection } = props;
  const selection = useSelection(props);
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

  // The anchor of a shift-click range: the tile toggled last. The range is
  // over the tiles as drawn (gridOrder), not over the list's order.
  const anchor = useRef<string | null>(null);
  useEffect(() => {
    anchor.current = null;
  }, [props.resetKey]);
  const toggle = (id: string, range: boolean) => {
    if (!selection || id === UP_ID) return;
    if (range && selection.mode === "ids" && anchor.current !== null) {
      const span = rangeIds(rows, anchor.current, id);
      const on = selection.isSelected(anchor.current);
      const ids = new Set(selection.ids);
      for (const x of span) {
        if (on) ids.add(x);
        else ids.delete(x);
      }
      selection.setIds([...ids]);
      return;
    }
    selection.toggle(id, { range });
    anchor.current = id;
  };
  const onGridKeyDown = (e: KeyboardEvent) => {
    if (!selection) return;
    if ((e.ctrlKey || e.metaKey) && !e.altKey && e.key.toLowerCase() === "a") {
      // Every loaded tile, ".." left out (selectableOnly).
      e.preventDefault();
      selection.setPage(true);
    } else if (e.key === "Escape" && !selection.isEmpty && !overlayOpen()) {
      e.preventDefault();
      selection.clear();
      anchor.current = null;
    }
  };

  return (
    <div className="tgrid-frame">
      <div className="tgrid-scroll" ref={scrollRef} data-testid="thumb-grid" onKeyDown={onGridKeyDown}>
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
          <div
            className="tgrid"
            style={style}
            data-compact={compact || undefined}
            data-selecting={selection ? (selection.isEmpty ? "idle" : "on") : undefined}
            role="list"
          >
            {rows.map((row) => (
              <Tile
                key={row.__meta.id}
                row={row}
                px={size}
                dir={isDir(row)}
                folder={folder}
                compact={compact}
                selected={selection ? selection.isSelected(String(row.__meta.id)) : undefined}
                onToggle={selection ? (range) => toggle(String(row.__meta.id), range) : undefined}
              />
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

interface TileProps {
  row: Row;
  px: number;
  dir: boolean;
  folder?: FolderNav;
  compact: boolean;
  /** Set while the view selects: is this tile selected? */
  selected?: boolean;
  /** Flips the tile's selection; `range` from the last one toggled (shift). */
  onToggle?: (range: boolean) => void;
}

function Tile({ row, px, dir, folder, compact, selected, onToggle }: TileProps) {
  const name = String(row.name ?? "");
  const up = dir && name === "..";
  const { t } = useI18n();
  const selectable = !!onToggle && !up;
  const path = folder ? String(row[folder.options.path_field] ?? "") : String(row.path ?? "");
  const preview = usePreview();
  const menu = useFileMenu();
  const openFolder = dir && folder ? () => folder.open(path) : undefined;
  const open = openFolder ?? (!dir && preview ? () => preview(row) : undefined);
  const onKeyDown = (e: KeyboardEvent) => {
    if (menu && !up && isMenuKey(e)) {
      // The keyboard's menu: under the tile (no pointer), not the browser's.
      e.preventDefault();
      menu(row, { clientX: 0, clientY: 0, target: e.currentTarget }, { openFolder });
    } else if (open && (e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      focusAfterOpen = dir;
      open();
    }
  };
  const onClick = (e: MouseEvent) => {
    // Ctrl/Cmd+click selects; a plain click opens, as before.
    if (selectable && (e.ctrlKey || e.metaKey)) {
      e.preventDefault();
      onToggle(false);
      return;
    }
    open?.();
  };
  const tr = useTr();
  const meta = up ? tr("Nazad", "Back") : dir ? "Folder" : [bytes(row.size as number | null), shortDate(row.mtime)].filter(Boolean).join(" · ");
  return (
    <div
      className={`tile${dir ? " dir" : ""}`}
      role="listitem"
      tabIndex={0}
      title={up ? tr("Gornji folder", "Parent folder") : name}
      aria-label={up ? tr("Gornji folder", "Parent folder") : dir ? `Folder ${name}` : name}
      data-testid={dir ? "tile-folder" : "tile-file"}
      data-path={path}
      data-selected={selectable && selected ? "true" : undefined}
      onClick={onClick}
      onKeyDown={onKeyDown}
      onContextMenu={
        menu && !up
          ? (e) => {
              e.preventDefault();
              menu(row, e, { openFolder });
            }
          : undefined
      }
    >
      {selectable ? (
        <input
          type="checkbox"
          className="tile-check"
          aria-label={t("APP", "SELECT_ROW")}
          checked={!!selected}
          onChange={() => {}}
          onClick={(e) => {
            // The checkbox selects; it never opens the preview or the folder.
            e.stopPropagation();
            onToggle(e.shiftKey);
          }}
          onKeyDown={(e) => e.stopPropagation()}
          onDoubleClick={(e) => e.stopPropagation()}
          data-testid="tile-select"
        />
      ) : null}
      <Thumb row={row} px={px} />
      <span className="tile-name">{name}</span>
      {compact ? null : <span className="tile-meta">{meta}</span>}
    </div>
  );
}

/** The slider in the toolbar while the Grid is open. */
function ThumbSizeTool() {
  const size = useThumbSize();
  const tr = useTr();
  const label = tr("Veličina sličica", "Thumbnail size");
  return (
    <label className="thumb-size" title={label}>
      <span>{label}</span>
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

/**
 * The context menu of a table row: the right-click (or the menu key) lands
 * anywhere in a <tr data-row-id>, which names the row of props.data.
 */
export function tableContextMenu(props: RendererProps, menu: OpenMenu | null) {
  if (!menu) return undefined;
  return (e: React.MouseEvent | React.KeyboardEvent) => {
    if ("key" in e && !isMenuKey(e)) return;
    const tr = (e.target as HTMLElement).closest?.("tr[data-row-id]");
    const id = tr?.getAttribute("data-row-id");
    const row = id == null ? undefined : props.data.rows.find((r) => String(r.__meta.id) === id);
    if (!row || String(row.name) === "..") return;
    e.preventDefault();
    const { folder } = props;
    const dir = folder ? isFolderRow(row, folder) : row.kind === "dir";
    const path = String(row.path ?? "");
    // From the keyboard there is no pointer: the menu opens under the row.
    const at = "key" in e ? { clientX: 0, clientY: 0, target: tr } : e;
    menu(row, at, { openFolder: dir && folder ? () => folder.open(path) : undefined });
  };
}

/** jsudc's table, plus the refresh while thumbnails are pending and the context menu. */
const Table = tableRenderer.component;
function LiveTable(props: RendererProps) {
  useLive(props);
  const menu = useFileMenu();
  const onMenu = tableContextMenu(props, menu);
  const selection = useSelection(props);
  return (
    <div className="ctx-scope" onContextMenu={onMenu} onKeyDown={onMenu}>
      <Table {...props} {...(selection ? { selection } : {})} />
    </div>
  );
}
export const filesTableRenderer: RendererDefinition = { ...tableRenderer, component: LiveTable };
