import { useUdcChanges, useUdcClient } from "@jsudc/react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { overview } from "../../shared/actions";
import { usePage, useApp, type Page } from "./App";
import { bytes, num } from "./format";
import { useTr } from "./i18n";
import { Activity, Download, Folder, LogOut, Users } from "./icons";
import { Files } from "./pages/Files";
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

export function Shell() {
  const [page, go] = usePage();
  const { me, signOut, paused } = useApp();
  const ov = useOverview();
  const tr = useTr();
  const qc = useQueryClient();
  // Every new log line can change the numbers of the header and the tiles.
  const { status } = useUdcChanges("logs", () => void qc.invalidateQueries({ queryKey: ["shfd-overview"] }));
  const host = location.hostname.split(".")[0] || location.hostname;
  const o = ov.data;

  const nav = (p: Page, label: string, icon: React.ReactNode, badge?: React.ReactNode) => (
    <a
      href={`#${p}`}
      className={page === p ? "on" : ""}
      onClick={(e) => {
        e.preventDefault();
        go(p);
      }}
    >
      {icon}
      {label}
      {badge ? <span className="badge-wrap" style={{ marginLeft: "auto" }}>{badge}</span> : null}
    </a>
  );

  return (
    <div id="app">
      <header className="top">
        <div className="logo">
          <div className="logo-mark">
            <Folder size={18} />
          </div>
          <div>
            <b>{host}</b>
            <small>shfd GUI</small>
          </div>
        </div>
        <div className="pill" title={me.share_id}>
          <span className="k">Folder</span> {me.share_name}
        </div>
        <div className="spacer" />
        {o ? (
          <span className={`status-pill ${o.server_ok ? "ok" : "err"}`}>
            <span className="dot" />
            {o.server_ok ? tr("shfd radi", "shfd is up") : tr("shfd ne radi", "shfd is down")} · v{o.version}
          </span>
        ) : ov.isError ? (
          <span className="status-pill err">{tr("shfd ne odgovara", "shfd is not answering")}</span>
        ) : null}
        <div className="user">
          <span className="avatar">{me.username.slice(0, 1).toUpperCase()}</span>
          <b>{me.username}</b>
          <span className="role">{me.role}</span>
        </div>
        <Prefs />
        <button className="btn ghost" onClick={signOut}>
          <LogOut size={16} /> {tr("Odjava", "Sign out")}
        </button>
      </header>
      <nav className="side">
        <div className="sec">{tr("Upravljanje", "Management")}</div>
        {nav("members", tr("Članovi i tokeni", "Members and tokens"), <Users size={16} />, o ? <span className="badge">{o.members}</span> : null)}
        <div className="sec">{tr("Sistem", "System")}</div>
        {nav("logs", tr("Logovi", "Logs"), <Activity size={16} />, <LivePill status={status} paused={paused} />)}
        {nav("files", tr("Fajlovi", "Files"), <Folder size={16} />)}
        <div className="sec">{tr("Pomoć", "Help")}</div>
        {nav("install", tr("Instalacija", "Install"), <Download size={16} />)}
        <div className="foot">
          <div className="row">
            <span>{tr("Zauzeto", "Used")}</span>
            <b>{o ? bytes(o.files_size) : "—"}</b>
          </div>
          <div className="row">
            <span>Trash</span>
            <b>{o ? bytes(o.trash_size) : "—"}</b>
          </div>
          <div className="row">
            <span>{tr("Poslednji seq", "Last seq")}</span>
            <b>{o ? num(o.head) : "—"}</b>
          </div>
        </div>
      </nav>
      <main>
        {page === "members" && <Members />}
        {page === "logs" && <Logs status={status} />}
        {page === "files" && <Files />}
        {page === "install" && <Install />}
      </main>
    </div>
  );
}
