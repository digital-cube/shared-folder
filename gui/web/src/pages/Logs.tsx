import type { FilterNode } from "@jsudc/core";
import { UdcDataView } from "@jsudc/react";
import { useState } from "react";
import { LivePill, useOverview } from "../Shell";
import { useApp } from "../App";
import { bytes, num } from "../format";
import { useTr } from "../i18n";
import { Pause, Play } from "../icons";

type Tab = "all" | "events" | "transfers" | "keys" | "errors";
const TABS: [Tab, string, string][] = [
  ["all", "Sve", "All"],
  ["events", "Izmene", "Changes"],
  ["transfers", "Transferi", "Transfers"],
  ["keys", "Ključevi", "Keys"],
  ["errors", "Greške", "Errors"],
];
const FILTERS: Record<Tab, FilterNode | undefined> = {
  all: undefined,
  events: { field: "kind", op: "eq", value: "izmena" },
  transfers: { field: "kind", op: "in", value: ["transfer", "odbijeno"] },
  keys: { field: "kind", op: "eq", value: "kljuc" },
  errors: { field: "error", op: "eq", value: true },
};

export function Logs({ status }: { status: string }) {
  const { paused, setPaused } = useApp();
  const tr = useTr();
  const [tab, setTab] = useState<Tab>("all");
  const ov = useOverview();
  const o = ov.data;
  const max = Math.max(1, ...(o?.log.per_member.map((m) => m.count) ?? [1]));

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h2>{tr("Logovi", "Logs")}</h2>
          <p>
            {tr("Šta se dešava na folderu, uživo iz", "What is happening on the folder, live from")} <code>service.log</code>.
          </p>
        </div>
        <div className="spacer" />
        <LivePill status={status} paused={paused} />
        <button className="btn" onClick={() => setPaused(!paused)}>
          {paused ? <Play size={16} /> : <Pause size={16} />} {paused ? tr("Nastavi", "Resume") : tr("Pauziraj", "Pause")}
        </button>
      </div>

      <div className="stats">
        <div className="stat">
          <div className="k">{tr("Događaja danas", "Events today")}</div>
          <div className="v">{o ? num(o.log.today) : "—"}</div>
          <div className="s">{o ? tr(`+${num(o.log.last_hour)} u poslednjih sat`, `+${num(o.log.last_hour)} in the last hour`) : ""}</div>
        </div>
        <div className="stat">
          <div className="k">{tr("Transferi (rsync)", "Transfers (rsync)")}</div>
          <div className="v">{o ? num(o.log.transfers) : "—"}</div>
          <div className="s">{o ? `${num(o.log.uploads)} upload · ${num(o.log.downloads)} download` : ""}</div>
        </div>
        <div className="stat">
          <div className="k">{tr("Odbijeno", "Refused")}</div>
          <div className="v" style={o && o.log.refused > 0 ? { color: "var(--g-err)" } : undefined}>
            {o ? num(o.log.refused) : "—"}
          </div>
          <div className="s">{tr("key refused / denied, danas", "key refused / denied, today")}</div>
        </div>
        {o && o.connected !== null ? (
          <div className="stat">
            <div className="k">{tr("Povezani klijenti", "Connected clients")}</div>
            <div className="v">{num(o.connected)}</div>
            <div className="s">{tr("WebSocket veze", "WebSocket connections")}</div>
          </div>
        ) : null}
      </div>

      <div className="split">
        <div>
          <div className="chips">
            {TABS.map(([t, sr, en]) => (
              <button key={t} className={`chip ${tab === t ? "on" : ""}`} onClick={() => setTab(t)}>
                {tr(sr, en)}
                {t === "errors" && o && o.log.refused > 0 ? <span className="n">{o.log.refused}</span> : null}
              </button>
            ))}
          </div>
          <div className="view-card short">
            <UdcDataView collection="logs" filter={FILTERS[tab]} urlState="memory" chrome="embedded" liveIndicator={false} />
          </div>
        </div>
        <div style={{ display: "grid", gap: 16, alignContent: "start" }}>
          <div className="card side-card">
            <h4>{tr("Aktivnost po članu (1 h)", "Activity per member (1 h)")}</h4>
            <div className="bars" style={{ paddingTop: 12 }}>
              {o && o.log.per_member.length > 0 ? (
                o.log.per_member.slice(0, 8).map((m) => (
                  <div className="bar" key={m.member}>
                    <span style={{ overflow: "hidden", textOverflow: "ellipsis" }}>{m.member}</span>
                    <span className="track">
                      <span className="fill" style={{ width: `${(100 * m.count) / max}%`, display: "block" }} />
                    </span>
                    <span>{m.count}</span>
                  </div>
                ))
              ) : (
                <span className="side-note">{tr("Nema aktivnosti u poslednjih sat vremena.", "No activity in the last hour.")}</span>
              )}
            </div>
          </div>
          <div className="card side-card">
            <h4>Server</h4>
            <div className="kv">
              <span className="k">{tr("Verzija", "Version")}</span>
              <span className="v mono">{o?.version ?? "—"}</span>
              <span className="k">Status</span>
              <span className="v">
                {o ? <span className={`badge ${o.server_ok ? "ok" : "err"}`}>{o.server_ok ? "ok" : tr("greška", "error")}</span> : "—"}
              </span>
              <span className="k">Head seq</span>
              <span className="v">{o ? num(o.head) : "—"}</span>
              <span className="k">{tr("Zauzeće", "Used")}</span>
              <span className="v">{o ? `${bytes(o.files_size)} · ${num(o.files)} ${tr("fajlova", "files")}` : "—"}</span>
              <span className="k">Trash</span>
              <span className="v">{o ? `${bytes(o.trash_size)} · ${num(o.trash)}` : "—"}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
