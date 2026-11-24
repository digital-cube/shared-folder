import { useUdcChanges, useUdcClient } from "@jsudc/react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { overview } from "../../shared/actions";
import { usePage, useApp, type Page } from "./App";
import { bytes, num } from "./format";
import { Activity, Download, Folder, LogOut, Users } from "./icons";
import { Files } from "./pages/Files";
import { Install } from "./pages/Install";
import { Logs } from "./pages/Logs";
import { Members } from "./pages/Members";

export function useOverview() {
  const client = useUdcClient();
  return useQuery({
    queryKey: ["shfd-overview"],
    queryFn: () => client.call(overview, {}),
    refetchInterval: 10_000,
  });
}

export function LivePill({ status, paused }: { status: string; paused: boolean }) {
  if (paused) return <span className="status-pill muted">pauzirano</span>;
  if (status === "live")
    return (
      <span className="status-pill ok live">
        <span className="dot" />
        live
      </span>
    );
  if (status === "connecting" || status === "reconnecting")
    return <span className="status-pill warn">reconnecting</span>;
  return <span className="status-pill muted">{status === "down" ? "veza prekinuta" : "off"}</span>;
}

export function Shell() {
  const [page, go] = usePage();
  const { me, signOut, paused } = useApp();
  const ov = useOverview();
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
            {o.server_ok ? "shfd radi" : "shfd ne radi"} · v{o.version}
          </span>
        ) : ov.isError ? (
          <span className="status-pill err">shfd ne odgovara</span>
        ) : null}
        <div className="user">
          <span className="avatar">{me.username.slice(0, 1).toUpperCase()}</span>
          <b>{me.username}</b>
          <span className="role">{me.role}</span>
        </div>
        <button className="btn ghost" onClick={signOut}>
          <LogOut size={16} /> Odjava
        </button>
      </header>
      <nav className="side">
        <div className="sec">Upravljanje</div>
        {nav("members", "Članovi i tokeni", <Users size={16} />, o ? <span className="badge">{o.members}</span> : null)}
        <div className="sec">Sistem</div>
        {nav("logs", "Logovi", <Activity size={16} />, <LivePill status={status} paused={paused} />)}
        {nav("files", "Fajlovi", <Folder size={16} />)}
        <div className="sec">Pomoć</div>
        {nav("install", "Instalacija", <Download size={16} />)}
        <div className="foot">
          <div className="row">
            <span>Zauzeto</span>
            <b>{o ? bytes(o.files_size) : "—"}</b>
          </div>
          <div className="row">
            <span>Trash</span>
            <b>{o ? bytes(o.trash_size) : "—"}</b>
          </div>
          <div className="row">
            <span>Poslednji seq</span>
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
