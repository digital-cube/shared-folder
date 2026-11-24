// The context menu of the Files page: a right-click (or the menu key,
// Shift+F10) on a row of the table or a tile of the Grid opens it at the
// pointer. A file gets Informacije, Prikaži, Preuzmi, Preimenuj and Obriši;
// a folder Otvori, Informacije and Obriši (filemenu.ts decides).
//
// Rename and remove are host actions (shfd.file_rename / shfd.file_remove)
// that make shfd's own sync ops with the admin's token, so every device of
// the folder renames or deletes the file too; a deleted file goes to the
// trash. FileActionsProvider also gives the cells the preview
// (PreviewContext), so one provider holds every dialog of a file.

import type { Row } from "@jsudc/core";
import { useUdcClient } from "@jsudc/react";
import { useToast } from "@jsudc/react/ui";
import { useQueryClient } from "@tanstack/react-query";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
} from "react";
import { removeFile, renameFile } from "../../shared/actions";
import { renameTarget } from "../../shared/rename";
import { getLang } from "./lang";
import { Modal } from "./dialogs";
import { FileMeta, FilePreview, PreviewContext } from "./FilePreview";
import { useTr } from "./i18n";
import { menuItems, placeMenu, stemRange, type MenuAction, type MenuItem } from "./filemenu";
import { Download, Eye, FolderOpen, Info, Pencil, Trash2 } from "./icons";
import { messageOf } from "./pages/Members";
import { contentUrl } from "./preview";
import { Thumb } from "./Thumb";

/** Where a menu was asked for: a mouse event, or the menu key on a focused element. */
export interface MenuAnchor {
  clientX: number;
  clientY: number;
  target: EventTarget | null;
}

/** Opens a row's menu; `openFolder` enters a folder row (the view's navigation). */
export type OpenMenu = (row: Row, at: MenuAnchor, opts?: { openFolder?: () => void }) => void;

const MenuContext = createContext<OpenMenu | null>(null);

/** Opens the context menu of a row; null outside the Files page. */
export function useFileMenu(): OpenMenu | null {
  return useContext(MenuContext);
}

const str = (v: unknown) => (v === null || v === undefined ? "" : String(v));

type Dialog = { type: "preview" | "info" | "rename" | "remove"; row: Row } | null;

interface OpenMenuState {
  row: Row;
  x: number;
  y: number;
  openFolder?: () => void;
  /** Gets the focus back when the menu closes without a dialog. */
  returnTo: HTMLElement | null;
}

/** Starts a download of a file, as the preview's Preuzmi does. */
function download(row: Row) {
  const a = document.createElement("a");
  a.href = contentUrl(str(row.path), true);
  a.download = str(row.name);
  a.rel = "noopener";
  document.body.appendChild(a);
  a.click();
  a.remove();
}

/** canEdit: Preimenuj and Obriši in the menu (an admin of the folder); without it only reading. */
export function FileActionsProvider({ children, canEdit = true }: { children: ReactNode; canEdit?: boolean }) {
  const [menu, setMenu] = useState<OpenMenuState | null>(null);
  const [dialog, setDialog] = useState<Dialog>(null);
  const preview = useCallback((row: Row) => setDialog({ type: "preview", row }), []);

  const openMenu = useCallback<OpenMenu>((row, at, opts) => {
    if (menuItems(row, canEdit).length === 0) return;
    const el = at.target instanceof HTMLElement ? at.target : null;
    let { clientX: x, clientY: y } = at;
    // The menu key has no pointer: open under the focused element.
    if (x === 0 && y === 0 && el) {
      const r = el.getBoundingClientRect();
      x = r.left + Math.min(24, r.width / 2);
      y = r.bottom;
    }
    setMenu({ row, x, y, openFolder: opts?.openFolder, returnTo: el?.closest<HTMLElement>("[tabindex], a, button") ?? el });
  }, [canEdit]);

  const choose = (m: OpenMenuState, action: MenuAction) => {
    setMenu(null);
    if (action === "open") m.openFolder?.();
    else if (action === "download") download(m.row);
    else if (action === "view") setDialog({ type: "preview", row: m.row });
    else setDialog({ type: action, row: m.row });
  };

  const close = () => setDialog(null);
  return (
    <PreviewContext.Provider value={preview}>
      <MenuContext.Provider value={openMenu}>
        {children}
        {menu ? (
          <ContextMenu
            state={menu}
            canEdit={canEdit}
            onChoose={(a) => choose(menu, a)}
            onClose={() => {
              setMenu(null);
              menu.returnTo?.focus({ preventScroll: true });
            }}
          />
        ) : null}
        {dialog?.type === "preview" ? <FilePreview row={dialog.row} onClose={close} /> : null}
        {dialog?.type === "info" ? <InfoDialog row={dialog.row} onClose={close} onView={() => setDialog({ type: "preview", row: dialog.row })} /> : null}
        {dialog?.type === "rename" ? <RenameDialog row={dialog.row} onClose={close} /> : null}
        {dialog?.type === "remove" ? <RemoveDialog row={dialog.row} onClose={close} /> : null}
      </MenuContext.Provider>
    </PreviewContext.Provider>
  );
}

const ICONS: Record<MenuAction, typeof Info> = {
  open: FolderOpen,
  info: Info,
  view: Eye,
  download: Download,
  rename: Pencil,
  remove: Trash2,
};

function ContextMenu({ state, canEdit, onChoose, onClose }: { state: OpenMenuState; canEdit: boolean; onChoose: (a: MenuAction) => void; onClose: () => void }) {
  useTr(); // the labels of menuItems follow the language
  const items = menuItems(state.row, canEdit);
  const ref = useRef<HTMLDivElement>(null);
  const [pos, setPos] = useState({ left: state.x, top: state.y, ready: false });

  // Measured once drawn (hidden), then kept on screen.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    setPos({ ...placeMenu(state.x, state.y, r.width, r.height, window.innerWidth, window.innerHeight), ready: true });
  }, [state.x, state.y]);
  // Once visible (a hidden element takes no focus), the first enabled item gets the focus.
  useEffect(() => {
    if (pos.ready) ref.current?.querySelector<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])')?.focus({ preventScroll: true });
  }, [pos.ready]);

  // Anything else (a click elsewhere, a scroll, a resize, leaving the window) closes it.
  useEffect(() => {
    const down = (e: Event) => {
      if (!ref.current?.contains(e.target as Node)) onClose();
    };
    const away = () => onClose();
    // Escape closes it wherever the focus is (the menu's own keys handle it first).
    const esc = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    const scroll = (e: Event) => {
      if (!ref.current?.contains(e.target as Node)) onClose();
    };
    document.addEventListener("mousedown", down, true);
    document.addEventListener("contextmenu", down, true);
    window.addEventListener("scroll", scroll, true);
    window.addEventListener("resize", away);
    window.addEventListener("blur", away);
    window.addEventListener("keydown", esc);
    return () => {
      window.removeEventListener("keydown", esc);
      document.removeEventListener("mousedown", down, true);
      document.removeEventListener("contextmenu", down, true);
      window.removeEventListener("scroll", scroll, true);
      window.removeEventListener("resize", away);
      window.removeEventListener("blur", away);
    };
  }, [onClose]);

  const onKeyDown = (e: ReactKeyboardEvent) => {
    const all = Array.from(ref.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not([aria-disabled="true"])') ?? []);
    const i = all.indexOf(document.activeElement as HTMLElement);
    const go = (n: number) => all[(n + all.length) % all.length]?.focus();
    if (e.key === "ArrowDown") go(i + 1);
    else if (e.key === "ArrowUp") go(i < 0 ? -1 : i - 1);
    else if (e.key === "Home") go(0);
    else if (e.key === "End") go(-1);
    else if (e.key === "Escape" || e.key === "Tab") onClose();
    else return;
    e.preventDefault();
    e.stopPropagation();
  };

  const run = (it: MenuItem) => {
    if (!it.disabled) onChoose(it.action);
  };
  return (
    <div
      ref={ref}
      className="ctx-menu"
      role="menu"
      aria-label={str(state.row.name)}
      data-testid="file-menu"
      style={{ left: pos.left, top: pos.top, visibility: pos.ready ? "visible" : "hidden" }}
      onKeyDown={onKeyDown}
      onContextMenu={(e) => e.preventDefault()}
    >
      <div className="ctx-title" title={str(state.row.path)}>
        {str(state.row.name)}
      </div>
      {items.map((it) => {
        const Icon = ICONS[it.action];
        return (
          <div key={it.action}>
            {it.separator ? <div className="ctx-sep" role="separator" /> : null}
            <button
              type="button"
              role="menuitem"
              className={`ctx-item${it.danger ? " danger" : ""}`}
              aria-disabled={it.disabled || undefined}
              title={it.hint}
              data-testid={`file-menu-${it.action}`}
              tabIndex={-1}
              onClick={() => run(it)}
            >
              <Icon size={15} />
              <span>{it.label}</span>
            </button>
          </div>
        );
      })}
    </div>
  );
}

function useEscape(onClose: () => void) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);
}

function InfoDialog({ row, onClose, onView }: { row: Row; onClose: () => void; onView: () => void }) {
  useEscape(onClose);
  const tr = useTr();
  const file = row.kind === "file";
  const viewable = menuItems(row).some((i) => i.action === "view" && !i.disabled);
  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal file-info" role="dialog" aria-label={`${tr("Informacije", "Info")}: ${str(row.name)}`} data-testid="file-info">
        <div className="file-info-head">
          <Thumb row={row} px={96} />
          <h3 title={str(row.path)}>{str(row.name)}</h3>
        </div>
        <FileMeta row={row} full />
        <div className="row">
          {viewable ? (
            <button className="btn" onClick={onView}>
              <Eye size={14} /> {tr("Prikaži", "View")}
            </button>
          ) : null}
          {file ? (
            <a className="btn" href={contentUrl(str(row.path), true)} download={str(row.name)}>
              <Download size={14} /> {tr("Preuzmi", "Download")}
            </a>
          ) : null}
          <button className="btn primary" onClick={onClose} autoFocus>
            {tr("Zatvori", "Close")}
          </button>
        </div>
      </div>
    </div>
  );
}

/** Runs a file action: success is a toast and a fresh list, failure stays in the dialog. */
function useFileAction() {
  const qc = useQueryClient();
  const { toast } = useToast();
  return async (f: () => Promise<{ message: string }>, onDone: () => void, setError: (e: string) => void) => {
    try {
      const r = await f();
      toast({ title: r.message, variant: "success" });
      onDone();
    } catch (err) {
      setError(messageOf(err));
    } finally {
      void qc.invalidateQueries();
    }
  };
}

function RenameDialog({ row, onClose }: { row: Row; onClose: () => void }) {
  const client = useUdcClient();
  const act = useFileAction();
  const path = str(row.path);
  const [name, setName] = useState(str(row.name));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const input = useRef<HTMLInputElement>(null);
  const tr = useTr();
  useEscape(onClose);
  useEffect(() => {
    const [a, b] = stemRange(name);
    input.current?.setSelectionRange(a, b);
    // Only when the dialog opens.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const target = renameTarget(path, name, getLang());
  const invalid = typeof target === "string" ? null : target.error;
  const same = target === path;
  return (
    <Modal title={tr("Preimenuj", "Rename")} onClose={onClose}>
      <p>
        {tr(
          "Novo ime važi na svim uređajima foldera. Fajl ostaje u istom folderu; isti sadržaj se ne šalje ponovo.",
          "The new name applies on every device of the folder. The file stays in the same folder; its content is not sent again.",
        )}
      </p>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          if (invalid || same || busy) return;
          setBusy(true);
          await act(() => client.call(renameFile, { path, name: name.trim(), base_hash: str(row.hash_full) }), onClose, setError);
          setBusy(false);
        }}
      >
        <div className="field">
          <label htmlFor="rename-name">{tr("Ime", "Name")}</label>
          <input
            id="rename-name"
            ref={input}
            className="input"
            value={name}
            onChange={(e) => {
              setName(e.target.value);
              setError(null);
            }}
            autoFocus
            spellCheck={false}
            data-testid="rename-input"
          />
          {invalid && name !== "" ? <span className="hint err-hint">{invalid}</span> : null}
        </div>
        {error ? (
          <div className="err-text" style={{ marginTop: 10 }} role="alert">
            {error}
          </div>
        ) : null}
        <div className="row">
          <button type="button" className="btn" onClick={onClose}>
            {tr("Otkaži", "Cancel")}
          </button>
          <button className="btn primary" disabled={busy || !!invalid || same} data-testid="rename-save">
            {tr("Preimenuj", "Rename")}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function RemoveDialog({ row, onClose }: { row: Row; onClose: () => void }) {
  const client = useUdcClient();
  const act = useFileAction();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const tr = useTr();
  useEscape(onClose);
  const dir = row.kind === "dir";
  const name = str(row.name);
  return (
    <Modal title={dir ? tr("Obriši folder", "Delete folder") : tr("Obriši fajl", "Delete file")} onClose={onClose}>
      {dir ? (
        <p>
          {tr("Folder", "The folder")} <b>{name}</b>{" "}
          {tr(
            "se briše sa svih uređaja foldera. Briše se samo prazan folder.",
            "is deleted from every device of the folder. Only an empty folder can be deleted.",
          )}
        </p>
      ) : (
        <p>
          <b>{name}</b>{" "}
          {tr(
            "se briše sa svih uređaja foldera. Ostaje u Trash-u, odakle se može vratiti.",
            "is deleted from every device of the folder. It stays in the Trash, where it can be restored from.",
          )}
        </p>
      )}
      {error ? (
        <div className="err-text" role="alert">
          {error}
        </div>
      ) : null}
      <div className="row">
        <button className="btn" onClick={onClose}>
          {tr("Otkaži", "Cancel")}
        </button>
        <button
          className="btn danger"
          autoFocus
          disabled={busy}
          data-testid="remove-confirm"
          onClick={async () => {
            setBusy(true);
            await act(
              () => client.call(removeFile, { path: str(row.path), kind: dir ? "dir" : "file", base_hash: str(row.hash_full) }),
              onClose,
              setError,
            );
            setBusy(false);
          }}
        >
          <Trash2 size={14} /> {tr("Obriši", "Delete")}
        </button>
      </div>
    </Modal>
  );
}
