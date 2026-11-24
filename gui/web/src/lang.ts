// The GUI's two languages, Serbian (Latin) and English.
//
// The texts live next to the code that shows them, as tr("srpski", "English"):
// both languages are read together, and a text cannot be missing in one of
// them. The current language is module state, so the plain helpers (format.ts,
// filemenu.ts, …) translate too; a component reads it through useTr() of
// i18n.tsx, which also re-renders it when the language changes.

export type Lang = "sr" | "en";
export const LANGS: readonly Lang[] = ["sr", "en"];
/** jsudc's LOCALE_STORAGE_KEY: one choice for the GUI and the library. (Not imported: this module stays free of React and the library.) */
export const LANG_STORAGE_KEY = "udc-locale";
/** The request header the GUI server answers in: its refusals come back in the UI's language. */
export const LANG_HEADER = "x-shfd-lang";

const storage = {
  get(k: string): string | null {
    try {
      return globalThis.localStorage?.getItem(k) ?? null;
    } catch {
      return null;
    }
  },
  set(k: string, v: string): void {
    try {
      globalThis.localStorage?.setItem(k, v);
    } catch {
      /* private mode, disabled storage */
    }
  },
};

/** The stored choice; Serbian when there is none. */
export function storedLang(get: (k: string) => string | null = storage.get): Lang {
  return get(LANG_STORAGE_KEY) === "en" ? "en" : "sr";
}

let current: Lang = storedLang();

export function getLang(): Lang {
  return current;
}

/** Switches the language of the plain helpers; the UI follows through <LangProvider>. */
export function setLang(l: Lang): void {
  current = l;
  storage.set(LANG_STORAGE_KEY, l);
  if (typeof document !== "undefined") document.documentElement.lang = htmlLang(l);
}

/** The text in the current language. */
export function tr(sr: string, en: string): string {
  return current === "en" ? en : sr;
}

/** The BCP 47 tag Intl formats numbers and dates with. */
export function intlLocale(l: Lang = current): string {
  return l === "en" ? "en-US" : "sr-Latn-RS";
}

export function htmlLang(l: Lang): string {
  return l === "en" ? "en" : "sr-Latn";
}
