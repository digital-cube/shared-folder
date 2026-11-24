import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { changePassword, revokeDevice, type Device, type PasswordRules } from "../api";
import { useAccount, useSession } from "../App";
import { Confirm } from "../dialogs";
import { ago, stamp } from "../format";
import { useTr } from "../i18n";
import { tr as trNow } from "../lang";
import { Folder, LogOut } from "../icons";
import { Prefs } from "../Prefs";

/** The rules of a new password as a hint: "Najmanje 12 znakova; ne sme biti korisničko ime." */
export function rulesHint(r: PasswordRules | undefined): string {
  const min = r?.min_len ?? 12;
  const parts = [trNow(`Najmanje ${min} znakova`, `At least ${min} characters`)];
  if (r?.max_len) parts[0] += trNow(`, najviše ${r.max_len}`, `, at most ${r.max_len}`);
  if (r?.not_username ?? true) parts.push(trNow("ne sme biti korisničko ime", "not the username"));
  if (r?.differ_from_old ?? true) parts.push(trNow("različita od stare", "different from the old one"));
  return parts.join("; ") + ".";
}

/**
 * What is wrong with a new password before it is sent, by the server's
 * rules; null when nothing is. The server checks again (and has the last word).
 */
export function passwordProblem(username: string, oldPw: string, newPw: string, repeat: string, r?: PasswordRules): string | null {
  const n = [...newPw].length;
  const min = r?.min_len ?? 12;
  if (n < min) return trNow(`Nova lozinka mora imati bar ${min} znakova.`, `The new password must have at least ${min} characters.`);
  if (r?.max_len && n > r.max_len) return trNow(`Nova lozinka može imati najviše ${r.max_len} znakova.`, `The new password may have at most ${r.max_len} characters.`);
  if ((r?.not_username ?? true) && newPw.toLowerCase() === username.toLowerCase())
    return trNow("Lozinka ne sme biti isto što i korisničko ime.", "The password may not be the username.");
  if (newPw === oldPw) return trNow("Nova lozinka mora da se razlikuje od stare.", "The new password must differ from the old one.");
  if (newPw !== repeat) return trNow("Ponovljena lozinka nije ista.", "The repeated password does not match.");
  return null;
}

/** "klijent" / "GUI sesija" for a device kind. */
export function kindLabel(kind: string): string {
  return kind === "gui" ? trNow("GUI sesija", "GUI session") : kind === "client" ? trNow("klijent (shf)", "client (shf)") : kind;
}

function PasswordForm({ forced, onDone }: { forced?: boolean; onDone?: () => void }) {
  const tr = useTr();
  const { me, setMe } = useSession();
  const account = useAccount();
  const rules = account.data?.password_rules;
  const [oldPw, setOld] = useState("");
  const [newPw, setNew] = useState("");
  const [repeat, setRepeat] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [busy, setBusy] = useState(false);
  const qc = useQueryClient();

  return (
    <form
      className="pw-form"
      data-testid="password-form"
      onSubmit={async (e) => {
        e.preventDefault();
        const problem = passwordProblem(me.username, oldPw, newPw, repeat, rules);
        if (problem) return setError(problem);
        setBusy(true);
        setError(null);
        const r = await changePassword(oldPw, newPw);
        setBusy(false);
        if (!r.ok) return setError(r.error);
        setOld("");
        setNew("");
        setRepeat("");
        setDone(true);
        void qc.invalidateQueries({ queryKey: ["account"] });
        onDone?.();
        setMe(r.data);
      }}
    >
      <input type="text" name="username" autoComplete="username" value={me.username} readOnly hidden />
      <div className="field">
        <label htmlFor="pw-old">{forced ? tr("Privremena lozinka", "Temporary password") : tr("Stara lozinka", "Old password")}</label>
        <input id="pw-old" className="input" type="password" autoComplete="current-password" value={oldPw} onChange={(e) => setOld(e.target.value)} autoFocus={forced} />
      </div>
      <div className="field">
        <label htmlFor="pw-new">{tr("Nova lozinka", "New password")}</label>
        <input id="pw-new" className="input" type="password" autoComplete="new-password" value={newPw} onChange={(e) => setNew(e.target.value)} />
        <span className="hint">{rulesHint(rules)}</span>
      </div>
      <div className="field">
        <label htmlFor="pw-repeat">{tr("Ponovi novu lozinku", "Repeat the new password")}</label>
        <input id="pw-repeat" className="input" type="password" autoComplete="new-password" value={repeat} onChange={(e) => setRepeat(e.target.value)} />
      </div>
      {error ? (
        <div className="err-text" role="alert">
          {error}
        </div>
      ) : null}
      {done && !forced ? (
        <div className="banner ok" role="status">
          {tr("Lozinka je promenjena.", "The password was changed.")}
        </div>
      ) : null}
      <button className={`btn primary ${forced ? "block" : ""}`} disabled={busy || !oldPw || !newPw || !repeat}>
        {busy ? tr("Menjam…", "Changing…") : tr("Promeni lozinku", "Change the password")}
      </button>
    </form>
  );
}

/** The screen of an account with a temporary password: nothing else until it is changed. */
export function ChangePassword({ forced }: { forced?: boolean }) {
  const tr = useTr();
  const { me, signOut } = useSession();
  return (
    <section id="login">
      <div className="login-prefs">
        <Prefs />
      </div>
      <div className="login-card" data-testid="must-change">
        <div className="logo">
          <div className="logo-mark">
            <Folder size={18} />
          </div>
          <div>
            <b>{me.username}</b>
            <small>{location.hostname}</small>
          </div>
        </div>
        <h1>{tr("Nova lozinka", "A new password")}</h1>
        <p className="sub">
          {tr(
            "Prijavio si se privremenom lozinkom. Pre svega ostalog izaberi svoju.",
            "You signed in with a temporary password. Choose your own before anything else.",
          )}
        </p>
        <PasswordForm forced={forced} />
        <div className="foot">
          <a
            href="#"
            onClick={(e) => {
              e.preventDefault();
              signOut();
            }}
          >
            <LogOut size={13} /> {tr("Odjava", "Sign out")}
          </a>
        </div>
      </div>
    </section>
  );
}

/** "Moj nalog": the account, the password change, and the account's devices with revoke. */
export function Account() {
  const tr = useTr();
  const { me, signedOut } = useSession();
  const account = useAccount();
  const qc = useQueryClient();
  const [confirm, setConfirm] = useState<Device | null>(null);
  const [banner, setBanner] = useState<{ ok: boolean; text: string } | null>(null);
  const devices = account.data?.devices ?? [];
  const user = account.data?.user;

  const revoke = async (d: Device) => {
    const r = await revokeDevice(d.id);
    if (!r.ok) {
      if (r.status === 401) return signedOut();
      return setBanner({ ok: false, text: r.error });
    }
    if (r.data.signed_out) return signedOut();
    setBanner({ ok: true, text: tr(`Uređaj ${d.name} je opozvan.`, `The device ${d.name} was revoked.`) });
    void qc.invalidateQueries({ queryKey: ["account"] });
  };

  return (
    <div className="page account-page">
      <div className="page-head">
        <div>
          <h2>{tr("Moj nalog", "My account")}</h2>
          <p>
            <b>{me.username}</b>
            {user?.display_name ? ` · ${user.display_name}` : ""}
            {user?.created_at ? <span className="muted"> · {tr("nalog od", "account since")} {stamp(user.created_at).slice(0, 10)}</span> : null}
          </p>
        </div>
      </div>
      {banner ? (
        <div className={`banner ${banner.ok ? "ok" : "err"}`} role="status">
          {banner.text}
        </div>
      ) : null}
      <div className="account-grid">
        <section className="card">
          <h4>{tr("Promena lozinke", "Change the password")}</h4>
          <PasswordForm />
        </section>
        <section className="card">
          <h4>{tr("Moji uređaji", "My devices")}</h4>
          <p className="muted small">
            {tr(
              "Klijenti (shf login) i GUI sesije prijavljene tvojim nalogom. Opoziv odmah gasi kredencijale uređaja u svim folderima; opoziv ove sesije te odjavljuje.",
              "Clients (shf login) and GUI sessions signed in with your account. Revoking ends the device's credentials in every folder at once; revoking this session signs you out.",
            )}
          </p>
          <table className="plain" data-testid="my-devices">
            <thead>
              <tr>
                <th>{tr("Ime", "Name")}</th>
                <th>{tr("Tip", "Type")}</th>
                <th>{tr("Poslednji put viđen", "Last seen")}</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {devices.map((d) => (
                <tr key={d.id}>
                  <td>
                    <b>{d.name}</b> {d.current ? <span className="badge ok">{tr("ova sesija", "this session")}</span> : null}
                    <div className="faint small">
                      {tr("prijavljen", "signed in")} {stamp(d.created_at)}
                      {d.expires_at ? ` · ${tr("ističe", "expires")} ${stamp(d.expires_at)}` : ""}
                    </div>
                  </td>
                  <td>
                    <span className={`badge ${d.kind === "gui" ? "info" : ""}`}>{kindLabel(d.kind)}</span>
                  </td>
                  <td title={stamp(d.last_seen_at)}>{ago(d.last_seen_at)}</td>
                  <td className="right">
                    <button className="btn sm danger" onClick={() => setConfirm(d)} data-testid={`revoke-device-${d.id}`}>
                      {tr("Opozovi", "Revoke")}
                    </button>
                  </td>
                </tr>
              ))}
              {account.data && devices.length === 0 ? (
                <tr>
                  <td colSpan={4} className="muted">
                    {tr("Nema uređaja.", "No devices.")}
                  </td>
                </tr>
              ) : null}
            </tbody>
          </table>
        </section>
      </div>
      {confirm ? (
        <Confirm
          title={tr(`Opozovi ${confirm.name}?`, `Revoke ${confirm.name}?`)}
          text={
            confirm.current
              ? tr("To je ova sesija: posle opoziva bićeš odjavljen.", "That is this session: after revoking it you will be signed out.")
              : confirm.kind === "client"
                ? tr(
                    "Klijent odmah gubi pristup svim folderima (API i rsync). Na tom uređaju treba ponovo shf login.",
                    "The client loses access to every folder at once (API and rsync). That device needs shf login again.",
                  )
                : tr("Ta GUI sesija se odmah odjavljuje.", "That GUI session is signed out at once.")
          }
          confirm={tr("Opozovi", "Revoke")}
          danger
          onClose={() => setConfirm(null)}
          onYes={() => void revoke(confirm)}
        />
      ) : null}
    </div>
  );
}
