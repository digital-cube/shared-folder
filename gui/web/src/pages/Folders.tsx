import { useState } from "react";
import { defaultPage } from "../access";
import { useAccount, usePage, useSession } from "../App";
import { ago, stamp } from "../format";
import { useTr } from "../i18n";
import { FolderOpen } from "../icons";
import { roleLabel } from "../Shell";

/** "Moji folderi": the folders of the account (GET /v1/me), each with its role and last change. */
export function Folders() {
  const tr = useTr();
  const { me, openFolder } = useSession();
  const account = useAccount();
  const [, go] = usePage();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const folders = account.data?.folders ?? [];

  const open = async (id: string) => {
    setBusy(id);
    setError(await openFolder(id));
    setBusy(null);
  };

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h2>{tr("Moji folderi", "My folders")}</h2>
          <p>
            {tr(
              "Folderi u kojima je tvoj nalog član. Izbor se pamti u ovom browseru; promeniš ga u zaglavlju.",
              "The folders your account is a member of. The choice is remembered in this browser; change it in the header.",
            )}
          </p>
        </div>
      </div>
      {error ? (
        <div className="banner err" role="alert">
          {error}
        </div>
      ) : null}
      {account.isError ? (
        <div className="banner err" role="alert">
          {(account.error as Error).message}
        </div>
      ) : null}
      {account.data && folders.length === 0 ? (
        <div className="card empty-card" data-testid="no-folders">
          <b>{tr("Nisi član nijednog foldera.", "You are not a member of any folder.")}</b>
          <p className="muted">
            {tr(
              "Admin foldera te dodaje po korisničkom imenu; posle toga folder se pojavljuje ovde.",
              "A folder's admin adds you by your username; the folder then shows up here.",
            )}
          </p>
        </div>
      ) : null}
      {folders.length > 0 ? (
        <div className="card table-card">
          <table className="plain" data-testid="folders-table">
            <thead>
              <tr>
                <th>{tr("Ime", "Name")}</th>
                <th>{tr("Uloga", "Role")}</th>
                <th>{tr("Poslednja promena", "Last change")}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {folders.map((f) => {
                const current = f.share_id === me.share_id;
                return (
                  <tr key={f.share_id} className={current ? "current" : ""}>
                    <td>
                      <span className="who">
                        <FolderOpen size={16} />
                        <b>{f.name}</b>
                        {current ? <span className="badge ok">{tr("otvoren", "open")}</span> : null}
                      </span>
                      <div className="faint mono small">{f.share_id}</div>
                    </td>
                    <td>
                      <span className={`badge ${f.role === "admin" ? "brand" : ""}`}>{roleLabel(f.role, tr)}</span>
                    </td>
                    <td title={stamp(f.last_change_at)}>{f.last_change_at ? ago(f.last_change_at) : tr("bez promena", "no changes")}</td>
                    <td className="right">
                      {current ? (
                        // Already open (the badge says so): the click goes to its first page.
                        <button className="btn sm" onClick={() => go(defaultPage(me))} data-testid={`show-${f.name}`}>
                          {tr("Prikaži", "Show")}
                        </button>
                      ) : (
                        <button className="btn sm primary" disabled={busy !== null} onClick={() => void open(f.share_id)} data-testid={`open-${f.name}`}>
                          {busy === f.share_id ? tr("Otvaram…", "Opening…") : tr("Otvori", "Open")}
                        </button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ) : null}
    </div>
  );
}
