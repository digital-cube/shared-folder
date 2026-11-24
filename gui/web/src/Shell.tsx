import { useUdcChanges, useUdcClient } from "@jsudc/react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { overview } from "../../shared/actions";
import { allowedPages, isAdmin, pageFor, type Page } from "./access";
import type { Me } from "./api";
import { useAccount, useApp, usePage, useSession } from "./App";
import { ago, bytes, num } from "./format";
import { useTr } from "./i18n";
import { Activity, Check, ChevronDown, Download, Folder, FolderOpen, LogOut, User, Users } from "./icons";
import { Account } from "./pages/Account";
import { Files } from "./pages/Files";
import { Folders } from "./pages/Folders";
import { Install } from "./pages/Install";
import { Logs } from "./pages/Logs";
import { Members } from "./pages/Members";
import { Prefs } from "./Prefs";

export function useOverview() {
  const client = useUdcClient();
  return useQuery({
    queryKey: ["shfd-overview"],
    queryFn: () => client.call(overview, {}),
    refetchInterval: 10_000,
  });
}

export function LivePill({ status, paused }: { status: string; paused: boolean }) {
  const tr = useTr();
  if (paused) return <span className="status-pill muted">{tr("pauzirano", "paused")}</span>;
  if (status === "live")
    return (
      <span className="status-pill ok live">
        <span className="dot" />
        live
      </span>
    );
  if (status === "connecting" || status === "reconnecting")
    return <span className="status-pill warn">reconnecting</span>;
  return <span className="status-pill muted">{status === "down" ? tr("veza prekinuta", "disconnected") : "off"}</span>;
}

/** "admin" / "član" for a role. */
export function roleLabel(role: string, tr: (sr: string, en: string) => string): string {
  return role === "admin" ? "admin" : tr("član", "member");
}

function NavLink({ p, page, label, icon, badge }: { p: Page; page: Page; label: string; icon: React.ReactNode; badge?: React.ReactNode }) {
  const [, go] = usePage();
  return (
    <a
      href={`#${p}`}
      className={page === p ? "on" : ""}
      data-testid={`nav-${p}`}
      onClick={(e) => {
        e.preventDefault();
        go(p);
      }}
    >
      {icon}
      {label}
      {badge ? (
        <span className="badge-wrap" style={{ marginLeft: "auto" }}>
          {badge}
        </span>
      ) : null}
    </a>
  );
}

function Logo() {
  const host = location.hostname.split(".")[0] || location.hostname;
  return (
    <div className="logo">
      <div className="logo-mark">
        <Folder size={18} />
      </div>
      <div>
        <b>{host}</b>
        <small>shfd GUI</small>
      </div>
    </div>
  );
}

/** The signed-in account (or member) and its role in the folder; a click opens Moj nalog. */
function UserBadge({ me }: { me: Me }) {
  const [, go] = usePage();
  const tr = useTr();
  const inner = (
    <>
      <span className="avatar">{me.username.slice(0, 1).toUpperCase()}</span>
      <b>{me.username}</b>
      {me.role ? <span className="role">{roleLabel(me.role, tr)}</span> : null}
    </>
  );
  if (me.kind !== "account") return <div className="user">{inner}</div>;
  return (
    <button type="button" className="user user-btn" title={tr("Moj nalog", "My account")} onClick={() => go("account")} data-testid="user-badge">
      {inner}
    </button>
  );
}

/**
 * The chosen folder in the header. For an account it is a menu of its
 * folders: choosing one switches to it; "Svi folderi" opens the list.
 */
export function FolderSwitch({ me }: { me: Me }) {
  const tr = useTr();
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [, go] = usePage();
  const { openFolder } = useSession();
  const account = useAccount(me.kind === "account");
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const away = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => e.key === "Escape" && setOpen(false);
    document.addEventListener("mousedown", away);
    window.addEventListener("keydown", esc);
    return () => {
      document.removeEventListener("mousedown", away);
      window.removeEventListener("keydown", esc);
    };
  }, [open]);

  if (me.kind !== "account") {
    return (
      <div className="pill" title={me.share_id}>
        <span className="k">Folder</span> {me.share_name}
      </div>
    );
  }
  const folders = account.data?.folders ?? [];
  return (
    <div className="folder-switch" ref={ref}>
      <button
        type="button"
        className="pill pill-btn"
        title={me.share_id}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
        data-testid="folder-switch"
      >
        <span className="k">Folder</span> {me.share_name || tr("nije izabran", "none chosen")}
        <ChevronDown size={14} />
      </button>
      {open ? (
        <div className="ctx-menu folder-menu" role="menu">
          {folders.map((f) => (
            <button
              key={f.share_id}
              type="button"
              role="menuitem"
              className={`ctx-item ${f.share_id === me.share_id ? "on" : ""}`}
              onClick={async () => {
                setOpen(false);
                if (f.share_id === me.share_id) return;
                setError(await openFolder(f.share_id));
              }}
            >
              <span className="check-slot">{f.share_id === me.share_id ? <Check size={14} /> : null}</span>
              <span className="grow">{f.name}</span>
              <span className={`badge ${f.role === "admin" ? "brand" : ""}`}>{roleLabel(f.role, tr)}</span>
            </button>
          ))}
          <div className="ctx-sep" role="separator" />
          <button
            type="button"
            role="menuitem"
            className="ctx-item"
            onClick={() => {
              setOpen(false);
              go("folders");
            }}
          >
            <span className="check-slot" />
            <span className="grow">{tr("Svi moji folderi…", "All my folders…")}</span>
          </button>
        </div>
      ) : null}
      {error ? (
        <span className="err-text inline" role="alert">
          {error}
        </span>
      ) : null}
    </div>
  );
}

export function Shell({ page }: { page: Page }) {
  const { me, signOut, paused } = useApp();
  const ov = useOverview();
  const tr = useTr();
  const qc = useQueryClient();
  const admin = isAdmin(me);
  const pages = allowedPages(me);
  // Every new log line can change the numbers of the header and the tiles
  // (an admin's only: a member has no logs).
  const { status } = useUdcChanges(admin ? "logs" : "files", () => void qc.invalidateQueries({ queryKey: ["shfd-overview"] }));
  const o = ov.data;

  return (
    <div id="app">
      <header className="top">
        <Logo />
        <FolderSwitch me={me} />
        <div className="spacer" />
        {o ? (
          <span className={`status-pill ${o.server_ok ? "ok" : "err"}`}>
            <span className="dot" />
            {o.server_ok ? tr("shfd radi", "shfd is up") : tr("shfd ne radi", "shfd is down")} · v{o.version}
          </span>
        ) : ov.isError ? (
          <span className="status-pill err">{tr("shfd ne odgovara", "shfd is not answering")}</span>
        ) : null}
        <UserBadge me={me} />
        <Prefs />
        <button className="btn ghost" onClick={signOut} data-testid="sign-out">
          <LogOut size={16} /> {tr("Odjava", "Sign out")}
        </button>
      </header>
      <nav className="side">
        {admin ? (
          <>
            <div className="sec">{tr("Upravljanje", "Management")}</div>
            <NavLink p="members" page={page} label={tr("Članovi", "Members")} icon={<Users size={16} />} badge={o && !o.limited ? <span className="badge">{o.members}</span> : null} />
            <div className="sec">{tr("Sistem", "System")}</div>
            <NavLink p="logs" page={page} label={tr("Logovi", "Logs")} icon={<Activity size={16} />} badge={<LivePill status={status} paused={paused} />} />
          </>
        ) : (
          <div className="sec">{tr("Folder", "Folder")}</div>
        )}
        {pages.includes("files") ? <NavLink p="files" page={page} label={tr("Fajlovi", "Files")} icon={<Folder size={16} />} /> : null}
        {me.kind === "account" ? (
          <>
            <div className="sec">{tr("Nalog", "Account")}</div>
            <NavLink p="folders" page={page} label={tr("Moji folderi", "My folders")} icon={<FolderOpen size={16} />} />
            <NavLink p="account" page={page} label={tr("Moj nalog", "My account")} icon={<User size={16} />} />
          </>
        ) : null}
        <div className="sec">{tr("Pomoć", "Help")}</div>
        <NavLink p="install" page={page} label={tr("Instalacija", "Install")} icon={<Download size={16} />} />
        {admin && o && !o.limited ? (
          <div className="foot">
            <div className="row">
              <span>{tr("Zauzeto", "Used")}</span>
              <b>{bytes(o.files_size)}</b>
            </div>
            <div className="row">
              <span>Trash</span>
              <b>{bytes(o.trash_size)}</b>
            </div>
            <div className="row">
              <span>{tr("Poslednji seq", "Last seq")}</span>
              <b>{num(o.head)}</b>
            </div>
          </div>
        ) : null}
      </nav>
      <main>
        {page === "members" && <Members />}
        {page === "logs" && <Logs status={status} />}
        {page === "files" && <Files />}
        {page === "folders" && <Folders />}
        {page === "account" && <Account />}
        {page === "install" && <Install />}
      </main>
    </div>
  );
}

/** An account without a chosen folder: Moji folderi, Moj nalog and Instalacija. */
export function AccountShell() {
  const { me, signOut } = useSession();
  const [asked] = usePage();
  const page = pageFor(me, asked);
  const tr = useTr();
  const account = useAccount();
  const n = account.data?.folders.length;
  return (
    <div id="app">
      <header className="top">
        <Logo />
        <div className="pill">
          <span className="k">Folder</span> {tr("nije izabran", "none chosen")}
        </div>
        <div className="spacer" />
        <UserBadge me={me} />
        <Prefs />
        <button className="btn ghost" onClick={signOut} data-testid="sign-out">
          <LogOut size={16} /> {tr("Odjava", "Sign out")}
        </button>
      </header>
      <nav className="side">
        <div className="sec">{tr("Nalog", "Account")}</div>
        <NavLink p="folders" page={page} label={tr("Moji folderi", "My folders")} icon={<FolderOpen size={16} />} badge={n !== undefined ? <span className="badge">{n}</span> : null} />
        <NavLink p="account" page={page} label={tr("Moj nalog", "My account")} icon={<User size={16} />} />
        <div className="sec">{tr("Pomoć", "Help")}</div>
        <NavLink p="install" page={page} label={tr("Instalacija", "Install")} icon={<Download size={16} />} />
        {account.data?.user.last_login_at ? (
          <div className="foot">
            <div className="row">
              <span>{tr("Prijava", "Signed in")}</span>
              <b>{ago(account.data.user.last_login_at)}</b>
            </div>
          </div>
        ) : null}
      </nav>
      <main>
        {page === "folders" && <Folders />}
        {page === "account" && <Account />}
        {page === "install" && <Install />}
      </main>
    </div>
  );
}
