import { useEffect, useState } from "react";
import { getConfig, signIn, signInWithToken, type Me } from "../api";
import { useFieldProblem } from "../fieldProblem";
import { loginProblem } from "../forms";
import { useTr } from "../i18n";
import { Folder } from "../icons";
import { Prefs } from "../Prefs";

/**
 * Sign-in with the account's username and password. "Prijava tokenom"
 * (a folder admin's member token, as before accounts) is a link under the
 * form while the server allows it (SHFD_GUI_TOKEN_LOGIN).
 */
export function Login({
  onIn,
  expired,
  notice,
  initialUsername = "",
}: {
  onIn: (m: Me) => void;
  expired: boolean;
  /** A note above the form ("the password was changed"). */
  notice?: string | null;
  initialUsername?: string;
}) {
  const tr = useTr();
  const [mode, setMode] = useState<"password" | "token">("password");
  const [tokenLogin, setTokenLogin] = useState(false);
  const [username, setUsername] = useState(initialUsername);
  const [password, setPassword] = useState("");
  const [token, setToken] = useState("");
  // The error of the last attempt, as the server said it; the expired note follows the language.
  const [error, setError] = useState<string | null>(null);
  const [showExpired, setShowExpired] = useState(expired);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    void getConfig().then((c) => setTokenLogin(c.token_login));
  }, []);

  const shown = error ?? (showExpired ? tr("Sesija je istekla ili je opozvana. Prijavi se ponovo.", "The session expired or was revoked. Sign in again.") : null);
  const fp = useFieldProblem({ username: "username", password: "password", token: "token" });

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (busy) return;
    if (fp.check(loginProblem(mode, username, password, token))) {
      setError(null);
      setShowExpired(false);
      return;
    }
    setBusy(true);
    setError(null);
    setShowExpired(false);
    const r = mode === "password" ? await signIn(username.trim(), password) : await signInWithToken(token.trim());
    setBusy(false);
    if (r.ok) {
      setPassword("");
      setToken("");
      onIn(r.data);
    } else {
      setError(r.error);
      if (mode === "password") setPassword("");
    }
  };

  const switchTo = (m: "password" | "token") => {
    setMode(m);
    setError(null);
    fp.clear();
  };

  return (
    <section id="login">
      <div className="login-prefs">
        <Prefs />
      </div>
      <form className="login-card" noValidate onSubmit={submit} autoComplete={mode === "password" ? "on" : "off"} data-testid="login-form">
        <div className="logo">
          <div className="logo-mark">
            <Folder size={18} />
          </div>
          <div>
            <b>{location.hostname.split(".")[0]}</b>
            <small>{location.hostname}</small>
          </div>
        </div>
        <h1>{mode === "password" ? tr("Prijava", "Sign in") : tr("Prijava tokenom", "Sign in with a token")}</h1>
        {mode === "password" ? (
          <>
            <p className="sub">{tr("Nalogom servera: tvoji folderi, fajlovi i uređaji.", "With your account on the server: your folders, files and devices.")}</p>
            <div className="field">
              <label htmlFor="username">{tr("Korisničko ime", "Username")}</label>
              <input
                id="username"
                name="username"
                className="input"
                autoComplete="username"
                autoCapitalize="none"
                spellCheck={false}
                value={username}
                onChange={(e) => {
                  setUsername(e.target.value);
                  fp.typed("username");
                }}
                autoFocus={!initialUsername}
                {...fp.aria("username")}
              />
              {fp.hint("username")}
            </div>
            <div className="field">
              <label htmlFor="password">{tr("Lozinka", "Password")}</label>
              <input
                id="password"
                name="password"
                className="input"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => {
                  setPassword(e.target.value);
                  fp.typed("password");
                }}
                autoFocus={!!initialUsername}
                {...fp.aria("password")}
              />
              {fp.hint("password")}
              <span className="hint">
                {tr("Nalog i privremenu lozinku daje admin servera.", "The server's admin gives you the account and a temporary password.")}{" "}
                <a href="#forgot" data-testid="forgot-link">
                  {tr("Zaboravljena lozinka?", "Forgot your password?")}
                </a>
              </span>
            </div>
          </>
        ) : (
          <>
            <p className="sub">{tr("Za stare članove bez naloga: token admina jednog foldera.", "For members without an account: the token of a folder admin.")}</p>
            <div className="field">
              <label htmlFor="token">Token</label>
              <input
                id="token"
                className="input mono"
                type="password"
                placeholder="shf_…"
                value={token}
                onChange={(e) => {
                  setToken(e.target.value);
                  fp.typed("token");
                }}
                autoFocus
                {...fp.aria("token")}
              />
              {fp.hint("token")}
              <span className="hint">
                {tr("Token člana foldera sa ulogom", "The token of a folder member with the role")} <b>admin</b>.
              </span>
            </div>
          </>
        )}
        {notice && !error ? (
          <div className="banner ok" role="status" data-testid="login-notice">
            {notice}
          </div>
        ) : null}
        {shown ? (
          <div className="err-text" role="alert">
            {shown}
          </div>
        ) : null}
        <button className="btn primary block" disabled={busy}>
          {busy ? tr("Proveravam…", "Checking…") : tr("Prijavi se", "Sign in")}
        </button>
        <div className="foot">
          <a href="#install">{tr("Kako se instalira klijent ili server", "How to install the client or the server")}</a>
          {tokenLogin || mode === "token" ? (
            <>
              {" · "}
              <a
                href="#"
                className="faint"
                data-testid="token-login-link"
                onClick={(e) => {
                  e.preventDefault();
                  switchTo(mode === "password" ? "token" : "password");
                }}
              >
                {mode === "password" ? tr("Prijava tokenom", "Sign in with a token") : tr("Prijava lozinkom", "Sign in with a password")}
              </a>
            </>
          ) : null}
        </div>
      </form>
    </section>
  );
}
