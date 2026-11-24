// The jsudc collections of shfd-gui: members, logs, files and trash. Their
// rows come from shfd's admin API with the token of the member who asks
// (the request context); every row carries share_id, which the row scope
// holds each session to.

import {
  SchemaDocSchema,
  ViewConfigSchema,
  type Capabilities,
  type FilterNode,
  type Row,
  type SortKey,
} from "@jsudc/core";
import {
  ArraySource,
  CollectionRegistry,
  READ_ONLY_ARRAY_CAPABILITIES,
  type ArrayLoad,
  type SourceContext,
  type SourceListRequest,
} from "@jsudc/server";
import { knownExtensions } from "../../shared/filekinds.js";
import type { LogFeed } from "./logs.js";
import { short, toRow } from "./logs.js";
import type { ShfdClient } from "./shfd.js";
import { toUdcError } from "./errors.js";
import type { Lang } from "./lang.js";

/** Who asks: the signed-in account (or member token) in its chosen folder. */
export interface Ctx {
  sessionId: string;
  kind: "account" | "token";
  /** The account's GUI device token, or the member token. */
  token: string;
  shareId: string;
  /** The account's username (the member's for a token session). */
  username: string;
  /** The member's name in this folder. */
  member: string;
  /** The role in this folder: "admin" or "member". */
  role: string;
  /** The UI's language (x-shfd-lang): refusals are sentences in it. */
  lang: Lang;
}

function ctxOf(sctx?: SourceContext): Ctx {
  return sctx?.ctx as Ctx;
}

/**
 * What a source does with a refusal of shfd before it becomes the
 * UdcError of the answer: a 401 ends the sessions of the token, a 403
 * makes the session a member of the folder (app.ts).
 */
export type OnRefused = (err: unknown, ctx: Ctx) => void;

/** The refusal as the UdcError of the answer, after the hook has seen it. */
function refuse(hook: OnRefused, e: unknown, c: Ctx): never {
  hook(e, c);
  throw toUdcError(e, c.lang);
}

function meta(id: string) {
  return { id, version: 1 };
}

// ---- members ---------------------------------------------------------------

class MembersSource extends ArraySource {
  readonly capabilities: Capabilities = { ...READ_ONLY_ARRAY_CAPABILITIES };
  protected readonly label = "collection members";
  protected readonly defaultSort: SortKey[] = [{ field: "created_at", dir: "asc" }];
  constructor(
    private readonly shfd: ShfdClient,
    schema: ConstructorParameters<typeof ArraySource>[0]["schema"],
    private readonly onRefused: OnRefused = () => {},
  ) {
    super({ schema });
  }
  protected async load(_req: SourceListRequest, sctx?: SourceContext): Promise<ArrayLoad> {
    const c = ctxOf(sctx);
    const l = await this.shfd.members(c.token, c.shareId).catch((e) => refuse(this.onRefused, e, c));
    return {
      rows: l.members.map((m) => ({
        __meta: meta(m.username),
        share_id: c.shareId,
        username: m.username,
        role: m.role,
        status: m.status,
        created_at: m.created_at,
        last_seen_at: m.last_seen_at,
        last_push_at: m.last_push_at,
        behind: m.status === "revoked" || m.status === "pending" ? null : m.behind,
        ssh_fingerprint: m.ssh_fingerprint ? short(m.ssh_fingerprint) : "",
        ssh_fingerprint_full: m.ssh_fingerprint,
        connected: m.connected,
        account: m.account ?? "",
        legacy: !m.account,
        devices: (m.devices ?? []).filter((d) => !d.revoked).length,
        me: m.username === c.member,
      })),
    };
  }
}

// ---- logs ------------------------------------------------------------------

class LogsSource extends ArraySource {
  readonly capabilities: Capabilities = { ...READ_ONLY_ARRAY_CAPABILITIES };
  protected readonly label = "collection logs";
  protected readonly defaultSort: SortKey[] = [{ field: "pos", dir: "desc" }];
  constructor(
    private readonly feed: LogFeed,
    schema: ConstructorParameters<typeof ArraySource>[0]["schema"],
    private readonly onRefused: OnRefused = () => {},
  ) {
    super({ schema, pageSizeDefault: 100 });
  }
  protected async load(_req: SourceListRequest, sctx?: SourceContext): Promise<ArrayLoad> {
    const c = ctxOf(sctx);
    const lines = await this.feed.lines(c.shareId, c.token).catch((e) => refuse(this.onRefused, e, c));
    return {
      rows: lines.map((l) => {
        const r = toRow(l);
        return { __meta: meta(r.id), share_id: c.shareId, pos: l.offset, ...r } as Row;
      }),
    };
  }
}

// ---- files -----------------------------------------------------------------

/**
 * Takes the folder the view is in (a `path eq <dir>` condition, anywhere in
 * the filter: the row scope wraps the client's filter) out of the filter.
 */
export function takePath(node?: FilterNode): { dir: string; rest: FilterNode | undefined } {
  let dir: string | null = null;
  const strip = (n: FilterNode): FilterNode | undefined => {
    if ("field" in n) {
      if (n.field !== "path") return n;
      if (n.op === "eq" && dir === null && typeof n.value === "string") dir = n.value;
      return undefined;
    }
    if ("not" in n) {
      const inner = strip(n.not);
      return inner && { not: inner };
    }
    const key = "and" in n ? "and" : "or";
    const kids = ((n as Record<string, FilterNode[]>)[key] ?? []).map(strip).filter((k): k is FilterNode => !!k);
    if (kids.length === 0) return undefined;
    if (kids.length === 1) return kids[0];
    return { [key]: kids } as FilterNode;
  };
  const rest = node ? strip(node) : undefined;
  return { dir: (dir ?? "").replace(/^\/+|\/+$/g, ""), rest };
}

export function extOf(name: string, kind: string): string | null {
  if (kind !== "file") return null;
  const i = name.lastIndexOf(".");
  return i > 0 ? name.slice(i + 1).toLowerCase() : null;
}

class FilesSource extends ArraySource {
  readonly capabilities: Capabilities = { ...READ_ONLY_ARRAY_CAPABILITIES };
  protected readonly label = "collection files";
  protected readonly defaultSort: SortKey[] = [
    { field: "kind", dir: "asc" },
    { field: "name", dir: "asc" },
  ];
  constructor(
    private readonly shfd: ShfdClient,
    schema: ConstructorParameters<typeof ArraySource>[0]["schema"],
    private readonly onRefused: OnRefused = () => {},
  ) {
    super({ schema, pageSizeDefault: 200 });
  }
  protected async load(req: SourceListRequest, sctx?: SourceContext): Promise<ArrayLoad> {
    const c = ctxOf(sctx);
    const { dir, rest } = takePath(req.filter);
    const l = await this.shfd.files(c.token, c.shareId, dir).catch((e) => refuse(this.onRefused, e, c));
    const rows = l.entries.map((f) => ({
      __meta: meta(f.path),
      share_id: c.shareId,
      path: f.path,
      name: f.name,
      kind: f.kind,
      ext: extOf(f.name, f.kind),
      size: f.kind === "file" ? f.size : null,
      mtime: f.mtime,
      updated_by: f.updated_by,
      hash: f.hash ? f.hash.slice(0, 12) : "",
      hash_full: f.hash,
      seq: f.seq,
      thumb: f.kind === "file" ? (f.thumb ?? "") : "",
    }));
    const head: Row[] = [];
    if (dir) {
      const parent = dir.includes("/") ? dir.slice(0, dir.lastIndexOf("/")) : "";
      head.push({
        __meta: meta(".."),
        share_id: c.shareId,
        path: parent,
        name: "..",
        kind: "dir",
        ext: null,
        size: null,
        mtime: null,
        updated_by: "",
        hash: "",
        hash_full: "",
        seq: null,
        thumb: "",
      } as Row);
    }
    return { rows: rows as Row[], filter: rest, head };
  }
}

// ---- trash -----------------------------------------------------------------

class TrashSource extends ArraySource {
  readonly capabilities: Capabilities = { ...READ_ONLY_ARRAY_CAPABILITIES };
  protected readonly label = "collection trash";
  protected readonly defaultSort: SortKey[] = [{ field: "at", dir: "desc" }];
  constructor(
    private readonly shfd: ShfdClient,
    schema: ConstructorParameters<typeof ArraySource>[0]["schema"],
    private readonly onRefused: OnRefused = () => {},
  ) {
    super({ schema });
  }
  protected async load(_req: SourceListRequest, sctx?: SourceContext): Promise<ArrayLoad> {
    const c = ctxOf(sctx);
    const l = await this.shfd.trash(c.token, c.shareId).catch((e) => refuse(this.onRefused, e, c));
    return {
      rows: (l.entries ?? []).map((t) => ({
        __meta: meta(String(t.id)),
        share_id: c.shareId,
        id: t.id,
        orig_path: t.orig_path,
        reason: t.reason,
        by: t.by,
        at: t.at,
        size: t.size,
      })) as Row[],
    };
  }
}

// ---- schemas and views -----------------------------------------------------

const membersSchema = SchemaDocSchema.parse({
  version: "v1",
  fields: {
    share_id: { type: "string" },
    username: { type: "string" },
    role: { type: "enum", values: ["admin", "member"] },
    status: { type: "enum", values: ["active", "pending", "revoked"] },
    created_at: { type: "datetime" },
    last_seen_at: { type: "datetime" },
    last_push_at: { type: "datetime" },
    behind: { type: "integer" },
    ssh_fingerprint: { type: "string" },
    ssh_fingerprint_full: { type: "string" },
    connected: { type: "integer" },
    account: { type: "string" },
    legacy: { type: "bool" },
    devices: { type: "integer" },
    me: { type: "bool" },
  },
});

const membersView = ViewConfigSchema.parse({
  default_renderer: "table",
  available_renderers: ["table"],
  page_size_default: 100,
  columns_mode: "listed",
  columns: [
    { field: "username", render: "member", width: 200 },
    { field: "account", render: "account", width: 150 },
    { field: "role", render: "role", width: 100 },
    { field: "status", render: "mstatus", width: 120 },
    { field: "last_seen_at", render: "ago", width: 150 },
    { field: "last_push_at", render: "ago", width: 150 },
    { field: "behind", render: "behind", align: "right", width: 90 },
    { field: "devices", render: "devices", align: "right", width: 100 },
    { field: "ssh_fingerprint", render: "fingerprint", width: "fill", min_width: 160 },
  ],
  search: { fields: ["username", "account"] },
  row_actions_display: "menu",
  row_action_defs: [
    { kind: "devices", label: "Uređaji", when: { field: "legacy", op: "eq", value: false } },
    {
      kind: "reset_token",
      label: "Nov token",
      when: { and: [{ field: "status", op: "neq", value: "revoked" }, { field: "legacy", op: "eq", value: true }] },
    },
    {
      kind: "make_admin",
      label: "Učini adminom",
      when: { and: [{ field: "status", op: "neq", value: "revoked" }, { field: "role", op: "eq", value: "member" }] },
    },
    {
      kind: "make_member",
      label: "Oduzmi admina",
      when: { and: [{ field: "status", op: "neq", value: "revoked" }, { field: "role", op: "eq", value: "admin" }] },
    },
    {
      kind: "revoke",
      label: "Opozovi",
      variant: "destructive",
      when: { field: "status", op: "neq", value: "revoked" },
    },
    { kind: "reactivate", label: "Vrati člana", when: { field: "status", op: "eq", value: "revoked" } },
  ],
});

const logsSchema = SchemaDocSchema.parse({
  version: "v1",
  fields: {
    share_id: { type: "string" },
    pos: { type: "integer" },
    ts: { type: "datetime" },
    kind: { type: "enum", values: ["izmena", "transfer", "kljuc", "sync", "odbijeno", "ostalo"] },
    member: { type: "string" },
    op: { type: "string" },
    path: { type: "string" },
    seq: { type: "integer" },
    outcome: { type: "string" },
    detail: { type: "string" },
    error: { type: "bool" },
    raw: { type: "string" },
  },
});

const logsView = ViewConfigSchema.parse({
  default_renderer: "table",
  available_renderers: ["table"],
  page_size_default: 100,
  columns_mode: "listed",
  default_sort: [{ field: "pos", dir: "desc" }],
  columns: [
    { field: "ts", render: "logtime", width: 172, sortable: false },
    { field: "kind", render: "logkind", width: 100, sortable: false },
    { field: "member", width: 110, sortable: false },
    { field: "op", render: "op", width: 110, sortable: false },
    { field: "path", render: "logpath", width: "fill", min_width: 200, sortable: false },
    { field: "seq", align: "right", width: 64, sortable: false },
  ],
  search: { fields: ["path", "member", "op"] },
});

const filesSchema = SchemaDocSchema.parse({
  version: "v1",
  fields: {
    share_id: { type: "string" },
    path: { type: "string" },
    name: { type: "string" },
    kind: { type: "enum", values: ["dir", "file"] },
    ext: { type: "string" },
    size: { type: "integer" },
    mtime: { type: "datetime" },
    updated_by: { type: "string" },
    hash: { type: "string" },
    hash_full: { type: "string" },
    seq: { type: "integer" },
    thumb: { type: "string" },
  },
});

const filesView = ViewConfigSchema.parse({
  default_renderer: "table",
  available_renderers: ["table", "thumbs"],
  page_size_default: 200,
  columns_mode: "listed",
  // jsudc's preview reads a filesystem source's /udc/files/blob, which this
  // collection has not: the name cell opens the GUI's own (FilePreview.tsx).
  folder_options: { preview: false },
  columns: [
    { field: "name", render: "filename", width: "fill", min_width: 260 },
    { field: "size", render: "bytes", align: "right", width: 110 },
    { field: "mtime", render: "datetime", width: 170 },
    { field: "updated_by", width: 130 },
    { field: "hash", render: "hash", width: 140 },
  ],
  search: { fields: ["name"] },
  // A select chip offers only the options listed here (jsudc reads no
  // distinct values): the extensions the type icons know.
  filters: [{ field: "ext", kind: "select", label: "Ekstenzija", options: knownExtensions() }],
});

const trashSchema = SchemaDocSchema.parse({
  version: "v1",
  fields: {
    share_id: { type: "string" },
    id: { type: "integer" },
    orig_path: { type: "string" },
    reason: { type: "enum", values: ["deleted", "replaced"] },
    by: { type: "string" },
    at: { type: "datetime" },
    size: { type: "integer" },
  },
});

const trashView = ViewConfigSchema.parse({
  default_renderer: "table",
  available_renderers: ["table"],
  page_size_default: 100,
  columns_mode: "listed",
  default_sort: [{ field: "at", dir: "desc" }],
  columns: [
    { field: "orig_path", width: "fill", min_width: 260 },
    { field: "reason", render: "reason", width: 120 },
    { field: "by", width: 120 },
    { field: "at", render: "datetime", width: 170 },
    { field: "size", render: "bytes", align: "right", width: 110 },
  ],
  search: { fields: ["orig_path", "by"] },
  row_action_defs: [{ kind: "restore", label: "Vrati" }],
});

export function buildRegistry(shfd: ShfdClient, feed: LogFeed, onRefused: OnRefused = () => {}): CollectionRegistry {
  const registry = new CollectionRegistry();
  registry.register({ name: "members", label: "Članovi", source: new MembersSource(shfd, membersSchema, onRefused), view: membersView });
  registry.register({ name: "logs", label: "Logovi", source: new LogsSource(feed, logsSchema, onRefused), view: logsView });
  registry.register({ name: "files", label: "Fajlovi", source: new FilesSource(shfd, filesSchema, onRefused), view: filesView });
  registry.register({ name: "trash", label: "Trash", source: new TrashSource(shfd, trashSchema, onRefused), view: trashView });
  return registry;
}
