import {
  createMemoryUrlStateAdapter,
  createRendererRegistry,
  UdcDataView,
  useUdcClient,
  type BulkActionContext,
  type UrlStateAdapter,
} from "@jsudc/react";
import { useQueryClient } from "@tanstack/react-query";
import type { Row } from "@jsudc/core";
import { useMemo, useState } from "react";
import { filesZip, restoreTrash, restoreTrashMany } from "../../../shared/actions";
import { inChunks, selectedRows, sumRestore } from "../bulk";
import { getLang } from "../lang";
import { isAdmin } from "../access";
import { useApp } from "../App";
import { useTr } from "../i18n";
import { Folder, Trash2, X } from "../icons";
import { messageOf } from "./Members";
import { BulkRemoveDialog, FileActionsProvider } from "../FileMenu";
import { filesTableRenderer, thumbsRenderer } from "../ThumbGrid";
import { savedFilesTab, saveFilesTab } from "../thumbsize";
import { contentUrl } from "../preview";
import { downloadPlan, filterOfState, listDir, startDownload, zipUrl } from "../zipDownload";

// The Files view's own tabs: the table (refreshing while thumbnails are
// pending) and the Grid. Trash keeps the provider's table.
const FILES_RENDERERS = createRendererRegistry(filesTableRenderer, thumbsRenderer);
const FILES_CHROME = { preset: "embedded", tabs: true } as const;

/**
 * The view's state in memory, as before, opening on the tab used last: the
 * tab is the view's `tab` parameter, so the store starts with it and every
 * write of the view keeps it.
 */
function filesUrlState(): UrlStateAdapter {
  const tab = savedFilesTab();
  const mem = createMemoryUrlStateAdapter(tab ? { tab } : undefined);
  return {
    get: () => mem.get(),
    subscribe: (l) => mem.subscribe(l),
    set: (next, opts) => {
      saveFilesTab(next.get("tab") ?? "table");
      mem.set(next, opts);
    },
  };
}

export function Files() {
  const [tab, setTab] = useState<"files" | "trash">("files");
  const client = useUdcClient();
  const qc = useQueryClient();
  const [banner, setBanner] = useState<{ ok: boolean; text: string } | null>(null);
  const filesState = useMemo(filesUrlState, []);
  const tr = useTr();
  const { me } = useApp();
  // A member who is not an admin browses and downloads: no Trash, no rename or delete.
  const admin = isAdmin(me);

  // Obriši of the Files bar: our confirmation, over the rows the selection
  // stands for (resolved while the dialog is open; ".." never among them).
  const [removing, setRemoving] = useState<{ rows: Row[] | null; clear: () => void } | null>(null);
  const filesActions = useMemo(
    () => ({
      // Preuzmi, for every member: one file as itself, anything else as one
      // ZIP. The folder is the list's (its filter's path condition), not a
      // row's; the selection stays, for what comes next.
      download: async (ctx: BulkActionContext) => {
        const rows = await selectedRows(client, ctx);
        const dir = listDir(ctx.filter ?? filterOfState(filesState.get()));
        const plan = downloadPlan(rows, dir, getLang());
        if (plan.kind === "error") throw new Error(plan.message);
        if (plan.kind === "file") {
          startDownload(contentUrl(plan.path, true), plan.name);
          return;
        }
        const zip = await client.call(filesZip, { dir: plan.dir, paths: plan.paths });
        startDownload(zipUrl(zip.id), zip.name);
      },
      remove: async (ctx: BulkActionContext) => {
        if (!ctx.filter) {
          setRemoving({ rows: await selectedRows(client, ctx), clear: ctx.clearSelection });
          return;
        }
        setRemoving({ rows: null, clear: ctx.clearSelection });
        try {
          const rows = await selectedRows(client, ctx);
          setRemoving((r) => (r ? { ...r, rows } : r));
        } catch (err) {
          setRemoving(null);
          throw err;
        }
      },
    }),
    [client, filesState],
  );

  // Vrati: a row's action (one entry, as before) or the bar's (the selection).
  const trashActions = {
    restore: async (ctx: BulkActionContext) => {
      const { row } = ctx;
      if (row) {
        try {
          const r = await client.call(restoreTrash, { id: String(row.__meta.id) });
          setBanner({ ok: true, text: r.message });
        } catch (err) {
          setBanner({ ok: false, text: messageOf(err) });
        } finally {
          void qc.invalidateQueries();
        }
        return;
      }
      try {
        const ids = (await selectedRows(client, ctx)).map((r) => String(r.__meta.id));
        const { parts, error } = await inChunks(ids, (chunk) => client.call(restoreTrashMany, { ids: chunk }));
        const sum = sumRestore(parts, getLang());
        if (parts.length > 0) ctx.clearSelection();
        const failed = sum.failed.length > 0 ? ` ${sum.failed.map((f) => f.message).filter((m, i, a) => a.indexOf(m) === i).join(" ")}` : "";
        const text = parts.length > 0 ? sum.message + failed : "";
        setBanner(error ? { ok: false, text: [text, messageOf(error)].filter(Boolean).join(" ") } : { ok: sum.failed.length === 0, text });
      } catch (err) {
        setBanner({ ok: false, text: messageOf(err) });
      } finally {
        void qc.invalidateQueries();
      }
    },
  };

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h2>{tr("Fajlovi", "Files")}</h2>
          <p>
            {tr("Sadržaj foldera kako ga server vidi", "The folder's content as the server sees it")} (<code>storage/</code>).{" "}
            {admin
              ? tr(
                  "Klik na fajl otvara pregled; desni klik (ili taster menija) daje informacije, pregled, preuzimanje, preimenovanje i brisanje. Izaberi više fajlova i foldera da ih preuzmeš kao jedan ZIP ili obrišeš odjednom.",
                  "A click on a file opens its preview; a right-click (or the menu key) gives info, view, download, rename and delete. Select several files and folders to download them as one ZIP or delete them at once.",
                )
              : tr(
                  "Klik na fajl otvara pregled; desni klik (ili taster menija) daje informacije, pregled i preuzimanje. Izaberi više fajlova i foldera da ih preuzmeš kao jedan ZIP. Menjaš ih na svom uređaju (shf).",
                  "A click on a file opens its preview; a right-click (or the menu key) gives info, view and download. Select several files and folders to download them as one ZIP. You change files on your device (shf).",
                )}
          </p>
        </div>
        <div className="spacer" />
        {admin ? (
        <div className="tabs">
          <button className={tab === "files" ? "on" : ""} onClick={() => setTab("files")}>
            <Folder size={15} /> {tr("Fajlovi", "Files")}
          </button>
          <button className={tab === "trash" ? "on" : ""} onClick={() => setTab("trash")}>
            <Trash2 size={15} /> Trash
          </button>
        </div>
        ) : null}
      </div>
      {banner ? (
        <div className={`banner ${banner.ok ? "ok" : "err"}`} role="status">
          {banner.text}
          <span className="spacer" />
          <button className="btn ghost sm" onClick={() => setBanner(null)} aria-label={tr("Zatvori", "Close")}>
            <X size={14} />
          </button>
        </div>
      ) : null}
      {removing ? (
        <BulkRemoveDialog rows={removing.rows} onDone={removing.clear} onClose={() => setRemoving(null)} />
      ) : null}
      {tab === "files" || !admin ? (
        <div className={`view-card files-view${admin ? "" : " read-only"}`}>
          <FileActionsProvider canEdit={admin}>
            <UdcDataView
              key="files"
              collection="files"
              urlState={filesState}
              chrome={FILES_CHROME}
              renderers={FILES_RENDERERS}
              selectable
              actions={filesActions}
            />
          </FileActionsProvider>
        </div>
      ) : (
        <>
          <p className="side-note" style={{ margin: "0 0 10px" }}>
            {tr("Obrisane i zamenjene verzije.", "Deleted and replaced versions.")} <b>{tr("Vrati", "Restore")}</b>{" "}
            {tr(
              "ih stavlja na staru putanju; ako je ona zauzeta drugim sadržajem, fajl dobija ime",
              "puts them back at their old path; when another content is there, the file is named",
            )}{" "}
            <code>.restored-…</code>. {tr("Trajno brisanje (purge) nije u GUI-ju.", "Permanent deletion (purge) is not in the GUI.")}
          </p>
          <div className="view-card">
            <UdcDataView key="trash" collection="trash" urlState="memory" chrome="embedded" actions={trashActions} />
          </div>
        </>
      )}
    </div>
  );
}
