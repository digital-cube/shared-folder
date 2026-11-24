import {
  createMemoryUrlStateAdapter,
  createRendererRegistry,
  UdcDataView,
  useUdcClient,
  type BulkActionContext,
  type UrlStateAdapter,
} from "@jsudc/react";
import { useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { restoreTrash } from "../../../shared/actions";
import { useTr } from "../i18n";
import { Folder, Trash2, X } from "../icons";
import { messageOf } from "./Members";
import { FileActionsProvider } from "../FileMenu";
import { filesTableRenderer, thumbsRenderer } from "../ThumbGrid";
import { savedFilesTab, saveFilesTab } from "../thumbsize";

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

  const actions = {
    restore: async ({ row }: BulkActionContext) => {
      try {
        const r = await client.call(restoreTrash, { id: String(row?.__meta.id ?? "") });
        setBanner({ ok: true, text: r.message });
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
            {tr(
              "Klik na fajl otvara pregled; desni klik (ili taster menija) daje informacije, pregled, preuzimanje, preimenovanje i brisanje.",
              "A click on a file opens its preview; a right-click (or the menu key) gives info, view, download, rename and delete.",
            )}
          </p>
        </div>
        <div className="spacer" />
        <div className="tabs">
          <button className={tab === "files" ? "on" : ""} onClick={() => setTab("files")}>
            <Folder size={15} /> {tr("Fajlovi", "Files")}
          </button>
          <button className={tab === "trash" ? "on" : ""} onClick={() => setTab("trash")}>
            <Trash2 size={15} /> Trash
          </button>
        </div>
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
      {tab === "files" ? (
        <div className="view-card files-view">
          <FileActionsProvider>
            <UdcDataView
              key="files"
              collection="files"
              urlState={filesState}
              chrome={FILES_CHROME}
              renderers={FILES_RENDERERS}
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
            <UdcDataView key="trash" collection="trash" urlState="memory" chrome="embedded" actions={actions} />
          </div>
        </>
      )}
    </div>
  );
}
