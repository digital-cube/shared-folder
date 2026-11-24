// The language of the browser's UI, for the sentences the server sends it.
// The web client says it in a header on every call (web/src/lang.ts).

export type Lang = "sr" | "en";

/** The request header the web client sends: "sr" or "en". */
export const LANG_HEADER = "x-shfd-lang";

/** The UI's language of a request: English only when the header says exactly "en"; Serbian otherwise. */
export function langOf(req: { headers: Record<string, string | string[] | undefined> }): Lang {
  const v = req.headers[LANG_HEADER];
  return (Array.isArray(v) ? v[0] : v)?.trim().toLowerCase() === "en" ? "en" : "sr";
}

/** The text in the language. */
export function tr(lang: Lang, sr: string, en: string): string {
  return lang === "en" ? en : sr;
}
