// What a bulk action on the Files page says it did, shared by the GUI
// server (one call's message) and the browser (the sum of several calls,
// web/src/bulk.ts): the same sentences either way.

export type BulkLang = "sr" | "en";

/** At most this many items per call of shfd.files_remove / shfd.trash_restore_many. */
export const BULK_MAX = 500;

/** The Serbian form of a count: 1 stavka, 2–4 stavke, 5+ stavki (11–14: stavki). */
export function srPlural(n: number, one: string, few: string, many: string): string {
  const d = n % 10;
  const dd = n % 100;
  if (d === 1 && dd !== 11) return one;
  if (d >= 2 && d <= 4 && (dd < 12 || dd > 14)) return few;
  return many;
}

const items = (lang: BulkLang, n: number) =>
  lang === "en" ? `${n} ${n === 1 ? "item" : "items"}` : `${n} ${srPlural(n, "stavka", "stavke", "stavki")}`;

export interface RemoveCounts {
  /** Removed now (a file went to the trash, an empty folder is gone). */
  removed: number;
  /** Already gone: nothing to do. */
  unchanged: number;
  /** Refused (a changed version, a folder not empty, …). */
  failed: number;
}

/** "Obrisano 12 stavki; fajlovi se mogu vratiti iz Trash-a." / "Obrisano 10 od 12; 2 nije moglo." */
export function removeSummary(lang: BulkLang, c: RemoveCounts): string {
  const tr = (sr: string, en: string) => (lang === "en" ? en : sr);
  const done = c.removed + c.unchanged;
  const total = done + c.failed;
  const gone = c.unchanged > 0 ? tr(` (${c.unchanged} već nije postojalo)`, ` (${c.unchanged} no longer existed)`) : "";
  if (c.failed === 0) {
    return tr(`Obrisano: ${items("sr", done)}${gone}; fajlovi se mogu vratiti iz Trash-a.`, `Deleted ${items("en", done)}${gone}; files can be restored from the Trash.`);
  }
  if (done === 0) return tr(`Ništa nije obrisano; ${c.failed} od ${total} nije moglo.`, `Nothing was deleted; ${c.failed} of ${total} could not be.`);
  return tr(`Obrisano ${done} od ${total}${gone}; ${c.failed} nije moglo.`, `Deleted ${done} of ${total}${gone}; ${c.failed} could not be.`);
}

export interface RestoreCounts {
  /** Back at the old path, or under a .restored-… name (counted in renamed too). */
  restored: number;
  /** Of restored: the path held other content, so the file got a .restored-… name. */
  renamed: number;
  /** The file already had this content. */
  unchanged: number;
  failed: number;
}

/** "Vraćeno 5 stavki (2 pod imenom .restored-…); 1 nije moglo." */
export function restoreSummary(lang: BulkLang, c: RestoreCounts): string {
  const tr = (sr: string, en: string) => (lang === "en" ? en : sr);
  const parts: string[] = [];
  if (c.restored > 0) {
    const renamed = c.renamed > 0 ? tr(` (${c.renamed} pod imenom .restored-…)`, ` (${c.renamed} named .restored-…)`) : "";
    parts.push(tr(`Vraćeno: ${items("sr", c.restored)}${renamed}`, `Restored ${items("en", c.restored)}${renamed}`));
  }
  if (c.unchanged > 0) parts.push(tr(`${c.unchanged} već ima taj sadržaj`, `${c.unchanged} already had that content`));
  if (c.failed > 0) parts.push(tr(`${c.failed} nije moglo`, `${c.failed} could not be restored`));
  if (parts.length === 0) return tr("Ništa nije vraćeno.", "Nothing was restored.");
  return `${parts.join("; ")}.`;
}
