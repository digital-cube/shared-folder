import { useState } from "react";
import { login, type Me } from "../api";
import { useTr } from "../i18n";
import { Folder } from "../icons";
import { Prefs } from "../Prefs";

export function Login({ onIn, expired }: { onIn: (m: Me) => void; expired: boolean }) {
  const tr = useTr();
  const [token, setToken] = useState("");
  // The error of the last attempt, as the server said it; the expired note follows the language.
  const [error, setError] = useState<string | null>(null);
  const [showExpired, setShowExpired] = useState(expired);
  const shown =
    error ?? (showExpired ? tr("Sesija je istekla ili je token opozvan. Prijavi se ponovo.", "The session expired or the token was revoked. Sign in again.") : null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token.trim()) return;
    setBusy(true);
    setError(null);
    setShowExpired(false);
    const r = await login(token.trim());
    setBusy(false);
    if (r.ok) {
      setToken("");
      onIn(r.me);
    } else setError(r.error);
  };

  return (
    <section id="login">
      <div className="login-prefs">
        <Prefs />
      </div>
      <form className="login-card" onSubmit={submit} autoComplete="off">
        <div className="logo">
          <div className="logo-mark">
            <Folder size={18} />
          </div>
          <div>
            <b>{location.hostname.split(".")[0]}</b>
            <small>{location.hostname}</small>
          </div>
        </div>
        <h1>{tr("Prijava", "Sign in")}</h1>
        <p className="sub">{tr("Za upravljanje folderom, logove i pregled fajlova.", "To manage the folder, read the logs and browse the files.")}</p>
        <div className="field">
          <label htmlFor="token">Token</label>
          <input
            id="token"
            className="input mono"
            type="password"
            placeholder="shf_…"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            autoFocus
          />
          <span className="hint">
            {tr("Token člana foldera. GUI mogu da koriste samo članovi sa ulogom", "The token of a folder member. Only members with the role")} <b>admin</b>
            {tr(".", " can use the GUI.")}
          </span>
        </div>
        {shown ? <div className="err-text" role="alert">{shown}</div> : null}
        <button className="btn primary block" disabled={busy || !token.trim()}>
          {busy ? tr("Proveravam…", "Checking…") : tr("Prijavi se", "Sign in")}
        </button>
        <div className="foot">
          <a href="#install">{tr("Kako se instalira klijent ili server", "How to install the client or the server")}</a>
        </div>
      </form>
    </section>
  );
}
