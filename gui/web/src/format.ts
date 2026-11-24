// Formatting in the UI's language (lang.ts): Serbian (Latin) or English.

import { intlLocale, tr } from "./lang";

const formats = new Map<string, Intl.NumberFormat>();
function nf(digits?: number): Intl.NumberFormat {
  const key = `${intlLocale()}/${digits ?? ""}`;
  let f = formats.get(key);
  if (!f) {
    f = new Intl.NumberFormat(intlLocale(), digits === undefined ? {} : { maximumFractionDigits: digits, minimumFractionDigits: digits });
    formats.set(key, f);
  }
  return f;
}

export function num(n: number | null | undefined): string {
  return n === null || n === undefined ? "—" : nf().format(n);
}

/** 12,4 GB (12.4 GB in English) */
export function bytes(n: number | null | undefined): string {
  if (n === null || n === undefined) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = n;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  const digits = i === 0 || v >= 100 ? 0 : 1;
  return `${nf(digits).format(v)} ${units[i]}`;
}

/** "pre 12 s", "pre 3 min", "pre 2 dana" ("12 s ago", "3 min ago", "2 days ago"); "—" for none. */
export function ago(ts: string | null | undefined, now = Date.now()): string {
  if (!ts) return "—";
  const t = Date.parse(ts);
  if (Number.isNaN(t)) return "—";
  const s = Math.max(0, Math.round((now - t) / 1000));
  if (s < 60) return tr(`pre ${s} s`, `${s} s ago`);
  const m = Math.round(s / 60);
  if (m < 60) return tr(`pre ${m} min`, `${m} min ago`);
  const h = Math.round(m / 60);
  if (h < 24) return tr(`pre ${h} h`, `${h} h ago`);
  const d = Math.round(h / 24);
  if (d < 7) return d === 1 ? tr("pre 1 dan", "1 day ago") : tr(`pre ${d} dana`, `${d} days ago`);
  const w = Math.round(d / 7);
  if (d < 60) return tr(`pre ${w} ned.`, w === 1 ? "1 week ago" : `${w} weeks ago`);
  const mo = Math.round(d / 30);
  return tr(`pre ${mo} mes.`, mo === 1 ? "1 month ago" : `${mo} months ago`);
}

const pad = (n: number) => String(n).padStart(2, "0");

/** 2026-11-24 12:16:10, local time */
export function stamp(ts: string | null | undefined): string {
  if (!ts) return "—";
  const d = new Date(ts);
  if (Number.isNaN(d.getTime())) return "—";
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** The shf join command for a new token. */
export function joinCommand(origin: string, shareId: string, token: string, folderName: string): string {
  const folder = `~/${folderName.replace(/[^A-Za-z0-9._-]+/g, "-") || "shared"}`;
  return `shf join ${origin} ${shareId} ${token} ${folder}`;
}
