import { useState } from "react";
import { login, type Me } from "../api";
import { Folder } from "../icons";

export function Login({ onIn, expired }: { onIn: (m: Me) => void; expired: boolean }) {
  const [token, setToken] = useState("");
  const [error, setError] = useState<string | null>(expired ? "Sesija je istekla ili je token opozvan. Prijavi se ponovo." : null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!token.trim()) return;
    setBusy(true);
    setError(null);
    const r = await login(token.trim());
    setBusy(false);
    if (r.ok) {
      setToken("");
      onIn(r.me);
    } else setError(r.error);
  };

  return (
    <section id="login">
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
        <h1>Prijava</h1>
        <p className="sub">Za upravljanje folderom, logove i pregled fajlova.</p>
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
            Token člana foldera. GUI mogu da koriste samo članovi sa ulogom <b>admin</b>.
          </span>
        </div>
        {error ? <div className="err-text" role="alert">{error}</div> : null}
        <button className="btn primary block" disabled={busy || !token.trim()}>
          {busy ? "Proveravam…" : "Prijavi se"}
        </button>
        <div className="foot">
          <a href="#install">Kako se instalira klijent ili server</a>
        </div>
      </form>
    </section>
  );
}
