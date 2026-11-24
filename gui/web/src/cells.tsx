// Cell components the views' columns name in `render`.

import { createCellRegistry, type CellProps } from "@jsudc/react";
import { ago, stamp } from "./format";
import { tr } from "./lang";
import { usePreview } from "./FilePreview";
import { Thumb } from "./Thumb";

const str = (v: unknown) => (v === null || v === undefined ? "" : String(v));

function Member({ value, row }: CellProps) {
  const name = str(value);
  const revoked = row.status === "revoked";
  return (
    <span className="who">
      <span className="avatar" style={revoked ? { background: "var(--g-bg-sunk)", color: "var(--g-faint)" } : undefined}>
        {name.slice(0, 1).toUpperCase()}
      </span>
      <b style={revoked ? { color: "var(--g-muted)" } : undefined}>{name}</b>
      {row.me ? <span className="faint">{tr("(ti)", "(you)")}</span> : null}
    </span>
  );
}

/** The account of a member, or "bez naloga" for one that has only a token. */
function AccountCell({ value, row }: CellProps) {
  const v = str(value);
  if (row.legacy) return <span className="badge warn" title={tr("Član radi starim tokenom, bez naloga.", "The member works with an old token, without an account.")}>{tr("bez naloga", "no account")}</span>;
  return <span>{v}</span>;
}

function Devices({ value, row }: CellProps) {
  if (row.legacy) return <span className="faint">—</span>;
  const n = Number(value ?? 0);
  return <span className={n > 0 ? "" : "faint"}>{n}</span>;
}

function Role({ value }: CellProps) {
  return <span className={`badge ${value === "admin" ? "brand" : ""}`}>{str(value)}</span>;
}

// [sr, en, tone, dot]
const STATUS: Record<string, [string, string, string, boolean]> = {
  active: ["aktivan", "active", "ok", true],
  pending: ["čeka join", "awaiting join", "warn", false],
  revoked: ["opozvan", "revoked", "err", false],
};

function MemberStatus({ value }: CellProps) {
  const [sr, en, tone, dot] = STATUS[str(value)] ?? [str(value), str(value), "", false];
  const label = tr(sr, en);
  return (
    <span className={`badge ${tone}`}>
      {dot ? <span className="dot" /> : null}
      {label}
    </span>
  );
}

function Ago({ value }: CellProps) {
  const v = str(value);
  return v ? <span title={stamp(v)}>{ago(v)}</span> : <span className="faint">—</span>;
}

function Behind({ value }: CellProps) {
  if (value === null || value === undefined) return <span className="faint">—</span>;
  const n = Number(value);
  return n > 0 ? (
    <span className="badge warn" title={tr("Događaji koje član još nije potvrdio", "Events the member has not confirmed yet")}>
      {n}
    </span>
  ) : (
    <span>0</span>
  );
}

function Fingerprint({ value, row }: CellProps) {
  const v = str(value);
  if (!v) return <span className="faint">{row.status === "revoked" ? "—" : tr("nema ključa", "no key")}</span>;
  return (
    <span className="mono muted" title={str(row.ssh_fingerprint_full)}>
      {v}
    </span>
  );
}

function LogTime({ value }: CellProps) {
  return <span className="mono muted" style={{ fontSize: 12, whiteSpace: "nowrap" }}>{stamp(str(value))}</span>;
}

// [sr, en, tone]
const KIND: Record<string, [string, string, string]> = {
  izmena: ["izmena", "change", "info"],
  transfer: ["transfer", "transfer", ""],
  kljuc: ["ključ", "key", "warn"],
  sync: ["sync", "sync", ""],
  odbijeno: ["odbijeno", "refused", "err"],
  ostalo: ["ostalo", "other", ""],
};

function LogKind({ value }: CellProps) {
  const [sr, en, tone] = KIND[str(value)] ?? [str(value), str(value), ""];
  return <span className={`badge ${tone}`}>{tr(sr, en)}</span>;
}

// CSS variables, so both themes have their own shade (proto.css).
export const OP_COLORS: Record<string, string> = {
  put: "var(--g-ok)",
  conflict: "var(--g-warn)",
  delete: "var(--g-err)",
  rmdir: "var(--g-err)",
  purge: "var(--g-err)",
  move: "var(--g-violet, #7c3aed)",
  mkdir: "var(--g-info)",
  restore: "var(--g-orange, #c2410c)",
  upload: "var(--g-text)",
  download: "var(--g-text)",
  denied: "var(--g-err)",
  failed: "var(--g-err)",
  "key-replaced": "var(--g-warn)",
  sync: "var(--g-muted)",
};

function Op({ value }: CellProps) {
  const v = str(value);
  return (
    <span className="op" style={{ color: OP_COLORS[v] ?? "var(--g-muted)" }}>
      {v || "—"}
    </span>
  );
}

function LogPath({ value, row }: CellProps) {
  const v = str(value) || (row.kind === "ostalo" ? str(row.raw) : "");
  const detail = (row.kind === "odbijeno" && row.op !== "sync") || row.kind === "ostalo" ? str(row.detail) : "";
  const full = detail ? `${v} · ${detail}` : v;
  return (
    <span className="mono" style={{ fontSize: 12.5, whiteSpace: "nowrap", overflow: "hidden", textOverflow: "ellipsis", display: "block" }} title={str(row.raw)}>
      {full}
    </span>
  );
}

function Hash({ value, row }: CellProps) {
  const v = str(value);
  return v ? (
    <span className="mono muted" title={str(row.hash_full)}>
      {v}…
    </span>
  ) : (
    <span className="faint">—</span>
  );
}

/**
 * The name column of Files: the thumbnail or the type icon, then the name.
 * jsudc wraps this column (folder_options.label_field) in its folder link,
 * so a click on a folder still opens it; its own small icon is hidden in
 * CSS (.files-view), this one stands in for it. A file's name is a button
 * that opens its preview (FilePreview.tsx).
 */
function FileName({ value, row }: CellProps) {
  const name = str(value);
  const open = usePreview();
  const file = row.kind === "file" && open;
  return (
    <span className="filename">
      <Thumb row={row} px={32} />
      {file ? (
        <button type="button" className="filename-text filename-open" title={name} onClick={() => open(row)} data-testid="file-open">
          {name}
        </button>
      ) : (
        <span className="filename-text" title={name}>
          {name}
        </span>
      )}
    </span>
  );
}

function Reason({ value }: CellProps) {
  return value === "deleted" ? (
    <span className="badge err">{tr("obrisan", "deleted")}</span>
  ) : (
    <span className="badge warn">{tr("zamenjen", "replaced")}</span>
  );
}

export const cells = createCellRegistry(
  { name: "member", component: Member },
  { name: "role", component: Role },
  { name: "account", component: AccountCell },
  { name: "devices", component: Devices },
  { name: "mstatus", component: MemberStatus },
  { name: "ago", component: Ago },
  { name: "behind", component: Behind },
  { name: "fingerprint", component: Fingerprint },
  { name: "logtime", component: LogTime },
  { name: "logkind", component: LogKind },
  { name: "op", component: Op },
  { name: "logpath", component: LogPath },
  { name: "hash", component: Hash },
  { name: "reason", component: Reason },
  { name: "filename", component: FileName },
);
