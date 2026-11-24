// Cell components the views' columns name in `render`.

import { createCellRegistry, type CellProps } from "@jsudc/react";
import { ago, stamp } from "./format";

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
      {row.me ? <span className="faint">(ti)</span> : null}
    </span>
  );
}

function Role({ value }: CellProps) {
  return <span className={`badge ${value === "admin" ? "brand" : ""}`}>{str(value)}</span>;
}

const STATUS: Record<string, [string, string, boolean]> = {
  active: ["aktivan", "ok", true],
  pending: ["čeka join", "warn", false],
  revoked: ["opozvan", "err", false],
};

function MemberStatus({ value }: CellProps) {
  const [label, tone, dot] = STATUS[str(value)] ?? [str(value), "", false];
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
    <span className="badge warn" title="Događaji koje član još nije potvrdio">
      {n}
    </span>
  ) : (
    <span>0</span>
  );
}

function Fingerprint({ value, row }: CellProps) {
  const v = str(value);
  if (!v) return <span className="faint">{row.status === "revoked" ? "—" : "nema ključa"}</span>;
  return (
    <span className="mono muted" title={str(row.ssh_fingerprint_full)}>
      {v}
    </span>
  );
}

function LogTime({ value }: CellProps) {
  return <span className="mono muted" style={{ fontSize: 12 }}>{stamp(str(value))}</span>;
}

const KIND: Record<string, [string, string]> = {
  izmena: ["izmena", "info"],
  transfer: ["transfer", ""],
  kljuc: ["ključ", "warn"],
  sync: ["sync", ""],
  odbijeno: ["odbijeno", "err"],
  ostalo: ["ostalo", ""],
};

function LogKind({ value }: CellProps) {
  const [label, tone] = KIND[str(value)] ?? [str(value), ""];
  return <span className={`badge ${tone}`}>{label}</span>;
}

export const OP_COLORS: Record<string, string> = {
  put: "#15803d",
  conflict: "#b45309",
  delete: "#b91c1c",
  rmdir: "#b91c1c",
  purge: "#b91c1c",
  move: "#7c3aed",
  mkdir: "#0369a1",
  restore: "#c2410c",
  upload: "#111827",
  download: "#111827",
  denied: "#b91c1c",
  failed: "#b91c1c",
  "key-replaced": "#b45309",
  sync: "#6b7280",
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
    <span className="mono" style={{ fontSize: 12.5 }} title={str(row.raw)}>
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

function Reason({ value }: CellProps) {
  return value === "deleted" ? <span className="badge err">obrisan</span> : <span className="badge warn">zamenjen</span>;
}

export const cells = createCellRegistry(
  { name: "member", component: Member },
  { name: "role", component: Role },
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
);
