import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { AVATAR_ACCEPT, avatarFileProblem, type AvatarFileProblem } from "../../../shared/avatar";
import { changePassword, removeAvatar, revokeDevice, setMyEmail, uploadAvatar, type Device, type PasswordRules } from "../api";
import { Avatar } from "../Avatar";
import { useAccount, useSession } from "../App";
import { Confirm } from "../dialogs";
import { useFieldProblem } from "../fieldProblem";
import { emailProblem, type FieldProblem } from "../forms";
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
 * What is wrong with the password form before it is sent, and in which
 * field, by the server's rules; null when nothing is. `forced` is the screen
 * of a required change, where the old password is the temporary one. The
 * server checks again (and has the last word).
 */
export function passwordProblem(
  username: string,
  oldPw: string,
  newPw: string,
  repeat: string,
  r?: PasswordRules,
  forced = false,
): FieldProblem<"old" | "new" | "repeat"> {
  const bad = (field: "old" | "new" | "repeat", text: string) => ({ field, text });
  if (!oldPw) return bad("old", forced ? trNow("Upiši privremenu lozinku.", "Enter the temporary password.") : trNow("Upiši staru lozinku.", "Enter the old password."));
  if (!newPw) return bad("new", trNow("Upiši novu lozinku.", "Enter the new password."));
  const n = [...newPw].length;
  const min = r?.min_len ?? 12;
  if (n < min) return bad("new", trNow(`Nova lozinka mora imati bar ${min} znakova.`, `The new password must have at least ${min} characters.`));
  if (r?.max_len && n > r.max_len) return bad("new", trNow(`Nova lozinka može imati najviše ${r.max_len} znakova.`, `The new password may have at most ${r.max_len} characters.`));
  if ((r?.not_username ?? true) && newPw.toLowerCase() === username.toLowerCase())
    return bad("new", trNow("Lozinka ne sme biti isto što i korisničko ime.", "The password may not be the username."));
  if (newPw === oldPw) return bad("new", trNow("Nova lozinka mora da se razlikuje od stare.", "The new password must differ from the old one."));
  if (!repeat) return bad("repeat", trNow("Ponovi novu lozinku.", "Repeat the new password."));
  if (newPw !== repeat) return bad("repeat", trNow("Ponovljena lozinka nije ista.", "The repeated password does not match."));
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
  const fp = useFieldProblem({ old: "pw-old", new: "pw-new", repeat: "pw-repeat" });

  return (
    <form
      className="pw-form"
      data-testid="password-form"
      noValidate
      onSubmit={async (e) => {
        e.preventDefault();
        if (busy) return;
        setDone(false);
        setError(null);
        if (fp.check(passwordProblem(me.username, oldPw, newPw, repeat, rules, forced))) return;
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
        <input
          id="pw-old"
          className="input"
          type="password"
          autoComplete="current-password"
          value={oldPw}
          onChange={(e) => {
            setOld(e.target.value);
            fp.typed("old");
          }}
          autoFocus={forced}
          {...fp.aria("old")}
        />
        {fp.hint("old")}
      </div>
      <div className="field">
        <label htmlFor="pw-new">{tr("Nova lozinka", "New password")}</label>
        <input
          id="pw-new"
          className="input"
          type="password"
          autoComplete="new-password"
          value={newPw}
          onChange={(e) => {
            setNew(e.target.value);
            fp.typed("new");
          }}
          {...fp.aria("new")}
        />
        {fp.hint("new")}
        <span className="hint">{rulesHint(rules)}</span>
      </div>
      <div className="field">
        <label htmlFor="pw-repeat">{tr("Ponovi novu lozinku", "Repeat the new password")}</label>
        <input
          id="pw-repeat"
          className="input"
          type="password"
          autoComplete="new-password"
          value={repeat}
          onChange={(e) => {
            setRepeat(e.target.value);
            fp.typed("repeat");
          }}
          {...fp.aria("repeat")}
        />
        {fp.hint("repeat")}
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
      <button className={`btn primary ${forced ? "block" : ""}`} disabled={busy}>
        {busy ? tr("Menjam…", "Changing…") : tr("Promeni lozinku", "Change the password")}
      </button>
    </form>
  );
}

/** The account's email address, where a forgotten password's link goes; changed with the password. */
function EmailForm({ current }: { current: string }) {
  const tr = useTr();
  const { me } = useSession();
  const qc = useQueryClient();
  const [email, setEmail] = useState<string | null>(null);
  const [pw, setPw] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const value = email ?? current;
  const changed = value.trim().toLowerCase() !== current;
  const fp = useFieldProblem({ addr: "email-addr", pw: "email-pw" });

  return (
    <form
      className="pw-form"
      data-testid="email-form"
      noValidate
      onSubmit={async (e) => {
        e.preventDefault();
        if (busy) return;
        if (fp.check(emailProblem(current, value, pw))) {
          setError(null);
          setDone(null);
          return;
        }
        setBusy(true);
        setError(null);
        setDone(null);
        const r = await setMyEmail(value.trim(), pw);
        setBusy(false);
        if (!r.ok) return setError(r.error);
        setPw("");
        setEmail(null);
        setDone(r.data.email ? tr(`Email je sada ${r.data.email}.`, `The email is now ${r.data.email}.`) : tr("Email je uklonjen.", "The email was removed."));
        void qc.invalidateQueries({ queryKey: ["account"] });
      }}
    >
      <input type="text" name="username" autoComplete="username" value={me.username} readOnly hidden />
      <div className="field">
        <label htmlFor="email-addr">{tr("Email adresa", "Email address")}</label>
        <input
          id="email-addr"
          className="input"
          type="email"
          autoComplete="email"
          placeholder={tr("nema", "none")}
          value={value}
          onChange={(e) => {
            setEmail(e.target.value);
            // The password's message depends on the address (add, change, remove), so any typing here takes both away.
            fp.clear();
          }}
          {...fp.aria("addr")}
        />
        {fp.hint("addr")}
        <span className="hint">
          {tr(
            "Na nju stiže link kad zaboraviš lozinku. Prazno polje uklanja adresu.",
            "The link for a forgotten password goes there. An empty field removes the address.",
          )}
        </span>
      </div>
      {changed ? (
        <div className="field">
          <label htmlFor="email-pw">{tr("Tvoja lozinka", "Your password")}</label>
          <input
            id="email-pw"
            className="input"
            type="password"
            autoComplete="current-password"
            value={pw}
            onChange={(e) => {
              setPw(e.target.value);
              fp.typed("pw");
            }}
            {...fp.aria("pw")}
          />
          {fp.hint("pw")}
          <span className="hint">{tr("Promenu adrese potvrđuješ lozinkom naloga.", "You confirm the change with your account password.")}</span>
        </div>
      ) : null}
      {error ? (
        <div className="err-text" role="alert">
          {error}
        </div>
      ) : null}
      {done ? (
        <div className="banner ok" role="status">
          {done}
        </div>
      ) : null}
      <button className="btn primary" disabled={busy}>
        {busy ? tr("Čuvam…", "Saving…") : tr("Sačuvaj email", "Save the email")}
      </button>
    </form>
  );
}

/** The sentence for a chosen file that is not sent. */
export function avatarProblemText(p: Exclude<AvatarFileProblem, null>): string {
  return p === "too_large"
    ? trNow("Slika je prevelika; najviše 5 MB.", "The picture is too large; 5 MB at most.")
    : trNow("Taj fajl nije slika u podržanom formatu (PNG, JPEG, WebP ili GIF).", "That file is not a picture in a supported format (PNG, JPEG, WebP or GIF).");
}

/**
 * The profile picture: the current one (or the initials), a file to choose
 * with a preview of it before it is saved, Sačuvaj and Ukloni. The server
 * cuts the middle square and makes it 256×256, so the preview is cut the
 * same way (object-fit: cover).
 */
export function AvatarForm({ username, displayName, current }: { username: string; displayName?: string; current: string | null }) {
  const tr = useTr();
  const { setMe, signedOut } = useSession();
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File | null>(null);
  const [preview, setPreview] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => () => void (preview && URL.revokeObjectURL(preview)), [preview]);

  const choose = (f: File | undefined) => {
    setDone(null);
    setError(null);
    if (input.current) input.current.value = "";
    if (!f) return;
    const problem = avatarFileProblem(f);
    if (problem) {
      setFile(null);
      setPreview(null);
      return setError(avatarProblemText(problem));
    }
    setFile(f);
    setPreview(URL.createObjectURL(f));
  };
  const finish = (r: Awaited<ReturnType<typeof removeAvatar>>, text: string) => {
    setBusy(false);
    if (!r.ok) {
      if (r.status === 401) return signedOut();
      return setError(r.error);
    }
    setFile(null);
    setPreview(null);
    setDone(text);
    setMe(r.data.session);
    void qc.invalidateQueries({ queryKey: ["account"] });
  };

  return (
    <div className="avatar-form" data-testid="avatar-form">
      <Avatar username={username} displayName={displayName} url={current} preview={preview} size={96} className="avatar-xl" />
      <div className="avatar-form-side">
        <input
          ref={input}
          id="avatar-file"
          type="file"
          accept={AVATAR_ACCEPT}
          hidden
          onChange={(e) => choose(e.target.files?.[0])}
          data-testid="avatar-file"
        />
        <div className="row-btns">
          <button type="button" className="btn" disabled={busy} onClick={() => input.current?.click()} data-testid="avatar-choose">
            {current || file ? tr("Izaberi drugu sliku…", "Choose another picture…") : tr("Izaberi sliku…", "Choose a picture…")}
          </button>
          {file ? (
            <>
              <button
                type="button"
                className="btn primary"
                disabled={busy}
                data-testid="avatar-save"
                onClick={async () => {
                  setBusy(true);
                  setError(null);
                  finish(await uploadAvatar(file), tr("Slika je sačuvana.", "The picture was saved."));
                }}
              >
                {busy ? tr("Čuvam…", "Saving…") : tr("Sačuvaj", "Save")}
              </button>
              <button
                type="button"
                className="btn ghost"
                disabled={busy}
                onClick={() => {
                  setFile(null);
                  setPreview(null);
                  setError(null);
                }}
              >
                {tr("Otkaži", "Cancel")}
              </button>
            </>
          ) : current ? (
            <button
              type="button"
              className="btn danger"
              disabled={busy}
              data-testid="avatar-remove"
              onClick={async () => {
                setBusy(true);
                setError(null);
                finish(await removeAvatar(), tr("Slika je uklonjena.", "The picture was removed."));
              }}
            >
              {tr("Ukloni", "Remove")}
            </button>
          ) : null}
        </div>
        <span className="hint">
          {file
            ? tr("Pregled: ovako će izgledati posle snimanja (kvadrat iz sredine slike).", "Preview: this is how it will look once saved (the square from the middle).")
            : tr("PNG, JPEG, WebP ili GIF, najviše 5 MB. Čuva se kvadrat iz sredine, 256×256.", "PNG, JPEG, WebP or GIF, 5 MB at most. The square from the middle is kept, 256×256.")}
        </span>
        {error ? (
          <div className="err-text" role="alert" data-testid="avatar-error">
            {error}
          </div>
        ) : null}
        {done ? (
          <div className="banner ok" role="status">
            {done}
          </div>
        ) : null}
      </div>
    </div>
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
        <Avatar username={me.username} displayName={user?.display_name} url={user ? (user.avatar_url ?? null) : me.avatar_url} size={48} />
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
        <div className="account-col">
          <section className="card">
            <h4>{tr("Profilna slika", "Profile picture")}</h4>
            <AvatarForm username={me.username} displayName={user?.display_name} current={user ? (user.avatar_url ?? null) : (me.avatar_url ?? null)} />
          </section>
          <section className="card">
            <h4>{tr("Email", "Email")}</h4>
            <EmailForm current={user?.email ?? ""} />
          </section>
          <section className="card">
            <h4>{tr("Promena lozinke", "Change the password")}</h4>
            <PasswordForm />
          </section>
        </div>
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
