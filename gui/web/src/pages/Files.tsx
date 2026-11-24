import { UdcDataView, useUdcClient, type BulkActionContext } from "@jsudc/react";
import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { restoreTrash } from "../../../shared/actions";
import { Folder, Trash2, X } from "../icons";
import { messageOf } from "./Members";

export function Files() {
  const [tab, setTab] = useState<"files" | "trash">("files");
  const client = useUdcClient();
  const qc = useQueryClient();
  const [banner, setBanner] = useState<{ ok: boolean; text: string } | null>(null);

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
          <h2>Fajlovi</h2>
          <p>
            Sadržaj foldera kako ga server vidi (<code>storage/</code>), samo za čitanje.
          </p>
        </div>
        <div className="spacer" />
        <div className="tabs">
          <button className={tab === "files" ? "on" : ""} onClick={() => setTab("files")}>
            <Folder size={15} /> Fajlovi
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
          <button className="btn ghost sm" onClick={() => setBanner(null)} aria-label="Zatvori">
            <X size={14} />
          </button>
        </div>
      ) : null}
      {tab === "files" ? (
        <div className="view-card">
          <UdcDataView key="files" collection="files" urlState="memory" chrome="embedded" />
        </div>
      ) : (
        <>
          <p className="side-note" style={{ margin: "0 0 10px" }}>
            Obrisane i zamenjene verzije. <b>Vrati</b> ih stavlja na staru putanju; ako je ona zauzeta drugim
            sadržajem, fajl dobija ime <code>.restored-…</code>. Trajno brisanje (purge) nije u GUI-ju.
          </p>
          <div className="view-card">
            <UdcDataView key="trash" collection="trash" urlState="memory" chrome="embedded" actions={actions} />
          </div>
        </>
      )}
    </div>
  );
}
