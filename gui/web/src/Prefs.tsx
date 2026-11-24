import { useTheme } from "@jsudc/react";
import { LANGS, useLang, useTr } from "./i18n";
import { Moon, Sun } from "./icons";

/** The language (SR | EN) and the light / dark theme, in the header and on the pages without one. */
export function Prefs() {
  const { lang, setLang } = useLang();
  const { theme, toggle } = useTheme();
  const tr = useTr();
  const dark = theme === "dark";
  return (
    <div className="prefs">
      <div className="lang-switch" role="group" aria-label={tr("Jezik", "Language")}>
        {LANGS.map((l) => (
          <button
            key={l}
            type="button"
            className={lang === l ? "on" : ""}
            aria-pressed={lang === l}
            lang={l === "en" ? "en" : "sr-Latn"}
            title={l === "en" ? "English" : "Srpski"}
            onClick={() => setLang(l)}
          >
            {l.toUpperCase()}
          </button>
        ))}
      </div>
      <button
        type="button"
        className="btn ghost icon theme-toggle"
        onClick={toggle}
        aria-pressed={dark}
        title={dark ? tr("Svetla tema", "Light theme") : tr("Tamna tema", "Dark theme")}
        aria-label={dark ? tr("Svetla tema", "Light theme") : tr("Tamna tema", "Dark theme")}
      >
        {dark ? <Sun size={16} /> : <Moon size={16} />}
      </button>
    </div>
  );
}
