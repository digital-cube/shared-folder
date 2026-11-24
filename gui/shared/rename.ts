// The new path of a renamed file, shared by the GUI server (the action)
// and the browser (the dialog says what is wrong before asking).

/**
 * The new path of a file renamed in its folder, or why the name will not
 * do, in the UI's language. Only the name changes here; shfd checks the
 * rest (invalid_path).
 */
export function renameTarget(path: string, name: string, lang: "sr" | "en" = "sr"): string | { error: string } {
  const tr = (sr: string, en: string) => (lang === "en" ? en : sr);
  const n = name.trim();
  if (!n) return { error: tr("Ime ne može biti prazno.", "The name cannot be empty.") };
  if (n === "." || n === "..") return { error: tr("Ime ne može biti . ni ..", "The name cannot be . or ..") };
  if (/[/\\\u0000]/.test(n)) return { error: tr("Ime ne sme da sadrži / ni \\.", "The name cannot contain / or \\.") };
  const i = path.lastIndexOf("/");
  return i < 0 ? n : `${path.slice(0, i + 1)}${n}`;
}
