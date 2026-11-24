import { useState, type ReactNode } from "react";
import { useTr } from "./i18n";

export function Modal({ title, children, onClose }: { title: string; children: ReactNode; onClose: () => void }) {
  return (
    <div className="modal-back" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-label={title}>
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

export function NewMember({ onAdd, onClose }: { onAdd: (name: string, admin: boolean) => Promise<string | null>; onClose: () => void }) {
  const [name, setName] = useState("");
  const [admin, setAdmin] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const tr = useTr();
  return (
    <Modal title={tr("Nov član", "New member")} onClose={onClose}>
      <p>
        {tr(
          "Jedan član = jedan uređaj. Za drugi uređaj ili drugu osobu napravi novog člana.",
          "One member = one device. For another device or another person, create a new member.",
        )}
      </p>
      <form
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          const err = await onAdd(name.trim(), admin);
          setBusy(false);
          if (err) setError(err);
          else onClose();
        }}
      >
        <div className="field">
          <label htmlFor="new-name">{tr("Ime", "Name")}</label>
          <input id="new-name" className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder={tr("npr. marko-laptop", "e.g. marko-laptop")} autoFocus />
          <span className="hint">{tr("Slova, brojevi, tačka, crtica i donja crta.", "Letters, digits, dot, dash and underscore.")}</span>
        </div>
        <label className="check">
          <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} /> {tr("Admin foldera", "Folder admin")}
        </label>
        {error ? <div className="err-text" style={{ marginTop: 10 }} role="alert">{error}</div> : null}
        <div className="row">
          <button type="button" className="btn" onClick={onClose}>
            {tr("Otkaži", "Cancel")}
          </button>
          <button className="btn primary" disabled={busy || !name.trim()}>
            {tr("Napravi", "Create")}
          </button>
        </div>
      </form>
    </Modal>
  );
}
