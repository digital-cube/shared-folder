import { useState } from "react";
import { forgotPassword, resetPassword } from "../api";
import { useFieldProblem } from "../fieldProblem";
import { forgotProblem } from "../forms";
import { useTr } from "../i18n";
import { ArrowLeft, Folder } from "../icons";
import { Prefs } from "../Prefs";
import { MIN_PASSWORD, resetProblem } from "../reset";

/** The frame of the pages shown without a sign-in: the language and theme, the card with the logo. */
function Card({ testid, children }: { testid: string; children: React.ReactNode }) {
  return (
    <section id="login">
      <div className="login-prefs">
        <Prefs />
      </div>
      <div className="login-card" data-testid={testid}>
        <div className="logo">
          <div className="logo-mark">
            <Folder size={18} />
          </div>
          <div>
            <b>{location.hostname.split(".")[0]}</b>
            <small>{location.hostname}</small>
          </div>
        </div>
        {children}
      </div>
    </section>
  );
}

function BackToLogin() {
  const tr = useTr();
  return (
    <div className="foot">
      <a href="#" data-testid="back-to-login">
        <ArrowLeft size={13} /> {tr("Nazad na prijavu", "Back to sign-in")}
      </a>
    </div>
  );
}

/**
 * "Zaboravljena lozinka": a username or an email address. The answer is
 * the same whether the account exists or not, so the page only ever says
 * "if it exists, a link was sent".
 */
export function ForgotPassword() {
  const tr = useTr();
  const [login, setLogin] = useState("");
  const [sent, setSent] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const fp = useFieldProblem({ login: "forgot-login" });

  if (sent) {
    return (
      <Card testid="forgot-sent">
        <h1>{tr("Proveri mejl", "Check your mail")}</h1>
        <div className="banner ok" role="status">
          {sent}
        </div>
        <BackToLogin />
      </Card>
    );
  }
  return (
    <Card testid="forgot-form">
      <form
        noValidate
        onSubmit={async (e) => {
          e.preventDefault();
          if (busy) return;
          if (fp.check(forgotProblem(login))) return setError(null);
          setBusy(true);
          setError(null);
          const r = await forgotPassword(login.trim());
          setBusy(false);
          if (r.ok) setSent(r.data.message);
          else setError(r.error);
        }}
      >
        <h1>{tr("Zaboravljena lozinka", "Forgotten password")}</h1>
        <p className="sub">
          {tr(
            "Upiši korisničko ime ili email naloga. Ako nalog ima email adresu, na nju stiže link za novu lozinku.",
            "Enter the username or the email of the account. If the account has an email address, a link for a new password is sent to it.",
          )}
        </p>
        <div className="field">
          <label htmlFor="forgot-login">{tr("Korisničko ime ili email", "Username or email")}</label>
          <input
            id="forgot-login"
            className="input"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            value={login}
            onChange={(e) => {
              setLogin(e.target.value);
              fp.typed("login");
            }}
            autoFocus
            {...fp.aria("login")}
          />
          {fp.hint("login")}
        </div>
        {error ? (
          <div className="err-text" role="alert">
            {error}
          </div>
        ) : null}
        <button className="btn primary block" disabled={busy}>
          {busy ? tr("Šaljem…", "Sending…") : tr("Pošalji link", "Send the link")}
        </button>
      </form>
      <BackToLogin />
    </Card>
  );
}

/**
 * Where the mailed link leads (#reset?token=…): the new password twice.
 * On success the sign-in page opens with a note.
 */
export function ResetPassword({ token, onDone }: { token: string; onDone: (username: string) => void }) {
  const tr = useTr();
  const [pw, setPw] = useState("");
  const [repeat, setRepeat] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const fp = useFieldProblem({ pw: "reset-new", repeat: "reset-repeat" });

  if (!token) {
    return (
      <Card testid="reset-no-token">
        <h1>{tr("Nova lozinka", "A new password")}</h1>
        <div className="err-text" role="alert">
          {tr("Link nema token. Otvori ceo link iz mejla, ili zatraži novi.", "The link has no token. Open the whole link from the mail, or ask for a new one.")}
        </div>
        <div className="foot">
          <a href="#forgot">{tr("Zatraži novi link", "Ask for a new link")}</a>
        </div>
      </Card>
    );
  }
  return (
    <Card testid="reset-form">
      <form
        noValidate
        onSubmit={async (e) => {
          e.preventDefault();
          if (busy) return;
          if (fp.check(resetProblem(pw, repeat))) return setError(null);
          setBusy(true);
          setError(null);
          const r = await resetPassword(token, pw);
          setBusy(false);
          if (!r.ok) return setError(r.error);
          setPw("");
          setRepeat("");
          onDone(r.data.username);
        }}
      >
        <h1>{tr("Nova lozinka", "A new password")}</h1>
        <p className="sub">
          {tr(
            "Izaberi novu lozinku naloga. Posle promene sve sesije naloga u browseru se odjavljuju.",
            "Choose a new password for the account. After the change every browser session of the account is signed out.",
          )}
        </p>
        <div className="field">
          <label htmlFor="reset-new">{tr("Nova lozinka", "New password")}</label>
          <input
            id="reset-new"
            className="input"
            type="password"
            autoComplete="new-password"
            value={pw}
            onChange={(e) => {
              setPw(e.target.value);
              fp.typed("pw");
            }}
            autoFocus
            {...fp.aria("pw")}
          />
          {fp.hint("pw")}
          <span className="hint">
            {tr(`Najmanje ${MIN_PASSWORD} znakova; ne sme biti korisničko ime.`, `At least ${MIN_PASSWORD} characters; not the username.`)}
          </span>
        </div>
        <div className="field">
          <label htmlFor="reset-repeat">{tr("Ponovi novu lozinku", "Repeat the new password")}</label>
          <input
            id="reset-repeat"
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
            {/nije ispravan|not valid/.test(error) ? (
              <>
                {" "}
                <a href="#forgot">{tr("Zatraži novi link.", "Ask for a new link.")}</a>
              </>
            ) : null}
          </div>
        ) : null}
        <button className="btn primary block" disabled={busy}>
          {busy ? tr("Menjam…", "Changing…") : tr("Postavi novu lozinku", "Set the new password")}
        </button>
      </form>
      <BackToLogin />
    </Card>
  );
}
