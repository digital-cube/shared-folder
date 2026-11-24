// The language as React state: <LangProvider> at the root, useTr() and
// useLang() in the components. The texts and plain helpers are in lang.ts.

import { createContext, useCallback, useContext, useState, type ReactNode } from "react";
import { getLang, setLang, tr, type Lang } from "./lang";

export { LANGS, tr, type Lang } from "./lang";

interface LangCtx {
  lang: Lang;
  setLang: (l: Lang) => void;
}
const LangContext = createContext<LangCtx>({ lang: "sr", setLang });

export function LangProvider({ children }: { children: ReactNode }) {
  const [lang, setState] = useState<Lang>(() => {
    const l = getLang();
    setLang(l);
    return l;
  });
  const change = useCallback((l: Lang) => {
    setLang(l);
    setState(l);
  }, []);
  return <LangContext.Provider value={{ lang, setLang: change }}>{children}</LangContext.Provider>;
}

export function useLang(): LangCtx {
  return useContext(LangContext);
}

/** tr, for a component: it re-renders when the language changes, and the function changes with it. */
export function useTr(): typeof tr {
  const { lang } = useContext(LangContext);
  return useCallback((sr: string, en: string) => (lang === "en" ? en : sr), [lang]);
}
