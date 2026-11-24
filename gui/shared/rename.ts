// The new path of a renamed file, shared by the GUI server (the action)
// and the browser (the dialog says what is wrong before asking).

/**
 * The new path of a file renamed in its folder, or why the name will not
 * do. Only the name changes here; shfd checks the rest (invalid_path).
 */
export function renameTarget(path: string, name: string): string | { error: string } {
  const n = name.trim();
  if (!n) return { error: "Ime ne može biti prazno." };
  if (n === "." || n === "..") return { error: "Ime ne može biti . ni .." };
  if (/[/\\\u0000]/.test(n)) return { error: "Ime ne sme da sadrži / ni \\." };
  const i = path.lastIndexOf("/");
  return i < 0 ? n : `${path.slice(0, i + 1)}${n}`;
}
