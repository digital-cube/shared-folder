import { useEffect, useState, type ReactNode } from "react";
import { useTr } from "./i18n";

export function Modal({ title, children, onClose, wide }: { title: string; children: ReactNode; onClose: () => void; wide?: boolean }) {
  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={`modal ${wide ? "wide" : ""}`} role="dialog" aria-label={title}>
        <h3>{title}</h3>
        {children}
      </div>
    </div>
  );
}

export function Confirm(props: {
  title: string;
  text: ReactNode;
  confirm: string;
  danger?: boolean;
  onYes: () => void;
  onClose: () => void;
}) {
  const tr = useTr();
  return (
    <Modal title={props.title} onClose={props.onClose}>
      <p>{props.text}</p>
      <div className="row">
        <button className="btn" onClick={props.onClose}>
          {tr("Otkaži", "Cancel")}
        </button>
        <button
          className={`btn ${props.danger ? "danger" : "primary"}`}
          autoFocus
          onClick={() => {
            props.onYes();
            props.onClose();
          }}
        >
          {props.confirm}
        </button>
      </div>
    </Modal>
  );
}

/** An account found by the search of "Dodaj člana". */
export interface UserRef {
  username: string;
  display_name: string;
}

/**
 * "Dodaj člana": an account, found by its username (GET /v1/users?q=), as
 * a member of this folder; its devices get their own credentials when they
 * join. Under "Napredno" the old way: a member with its own token, for a
 * client without an account.
 */
export function AddMember({
  search,
  onAddAccount,
  onAddToken,
  onClose,
}: {
  search: (q: string) => Promise<UserRef[]>;
  onAddAccount: (user: string, admin: boolean) => Promise<string | null>;
  onAddToken: (name: string, admin: boolean) => Promise<string | null>;
  onClose: () => void;
}) {
  const tr = useTr();
  const [q, setQ] = useState("");
  const [found, setFound] = useState<UserRef[] | null>(null);
  const [picked, setPicked] = useState<UserRef | null>(null);
  const [admin, setAdmin] = useState(false);
  const [advanced, setAdvanced] = useState(false);
  const [name, setName] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // The search follows the typing, a moment after it stops.
  useEffect(() => {
    if (advanced) return;
    const term = q.trim();
    if (!term) {
      setFound(null);
      return;
    }
    let live = true;
    const t = setTimeout(() => {
      search(term).then(
        (users) => live && setFound(users),
        (err) => live && setError((err as Error).message),
      );
    }, 200);
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [q, advanced, search]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    const err = advanced ? await onAddToken(name.trim(), admin) : picked ? await onAddAccount(picked.username, admin) : null;
    setBusy(false);
    if (err) setError(err);
    else onClose();
  };

  return (
    <Modal title={tr("Dodaj člana", "Add a member")} onClose={onClose}>
      <form onSubmit={submit} data-testid="add-member">
        {!advanced ? (
          <>
            <p>
              {tr(
                "Član je nalog na serveru. Svaki njegov uređaj dobija svoje kredencijale kad se priključi (shf login, pa shf join).",
                "A member is an account on the server. Each of its devices gets its own credentials when it joins (shf login, then shf join).",
              )}
            </p>
            <div className="field">
              <label htmlFor="user-q">{tr("Nalog", "Account")}</label>
              <input
                id="user-q"
                className="input"
                value={picked ? picked.username : q}
                onChange={(e) => {
                  setPicked(null);
                  setQ(e.target.value);
                }}
                placeholder={tr("korisničko ime…", "username…")}
                autoComplete="off"
                autoFocus
              />
              {!picked && found ? (
                <div className="user-results" role="listbox" data-testid="user-results">
                  {found.length === 0 ? (
                    <div className="faint small">{tr("Nema naloga sa tim imenom.", "No account by that name.")}</div>
                  ) : (
                    found.map((u) => (
                      <button type="button" role="option" aria-selected={false} key={u.username} className="ctx-item" onClick={() => setPicked(u)}>
                        <span className="avatar sm">{u.username.slice(0, 1).toUpperCase()}</span>
                        <b>{u.username}</b>
                        {u.display_name ? <span className="muted">{u.display_name}</span> : null}
                      </button>
                    ))
                  )}
                </div>
              ) : null}
              <span className="hint">
                {tr("Naloge pravi admin servera (shfd user add); ovde se samo dodaju u folder.", "Accounts are made by the server's admin (shfd user add); here they are only added to the folder.")}
              </span>
            </div>
          </>
        ) : (
          <>
            <p>
              {tr(
                "Član bez naloga, sa svojim tokenom: za stari klijent koji se priključuje tokenom. Jedan takav član = jedan uređaj.",
                "A member without an account, with its own token: for an old client that joins with a token. One such member = one device.",
              )}
            </p>
            <div className="field">
              <label htmlFor="new-name">{tr("Ime", "Name")}</label>
              <input id="new-name" className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder={tr("npr. marko-laptop", "e.g. marko-laptop")} autoFocus />
              <span className="hint">{tr("Slova, brojevi, tačka, crtica i donja crta.", "Letters, digits, dot, dash and underscore.")}</span>
            </div>
          </>
        )}
        <label className="check">
          <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} /> {tr("Admin foldera", "Folder admin")}
        </label>
        <details
          className="advanced"
          open={advanced}
          onToggle={(e) => {
            setAdvanced((e.target as HTMLDetailsElement).open);
            setError(null);
          }}
        >
          <summary>{tr("Napredno: član sa tokenom (bez naloga)", "Advanced: a member with a token (no account)")}</summary>
        </details>
        {error ? (
          <div className="err-text" style={{ marginTop: 10 }} role="alert">
            {error}
          </div>
        ) : null}
        <div className="row">
          <button type="button" className="btn" onClick={onClose}>
            {tr("Otkaži", "Cancel")}
          </button>
          <button className="btn primary" disabled={busy || (advanced ? !name.trim() : !picked)} data-testid="add-member-submit">
            {advanced ? tr("Napravi token", "Create a token") : tr("Dodaj", "Add")}
          </button>
        </div>
      </form>
    </Modal>
  );
}
