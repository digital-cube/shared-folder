import type { FilterNode } from "@jsudc/core";
import { UdcDataView } from "@jsudc/react";
import { useState } from "react";
import { LivePill, useOverview } from "../Shell";
import { useApp } from "../App";
import { bytes, num } from "../format";
import { Pause, Play } from "../icons";

type Tab = "all" | "events" | "transfers" | "keys" | "errors";
const TABS: [Tab, string][] = [
  ["all", "Sve"],
  ["events", "Izmene"],
  ["transfers", "Transferi"],
  ["keys", "Ključevi"],
  ["errors", "Greške"],
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
  const [tab, setTab] = useState<Tab>("all");
  const ov = useOverview();
  const o = ov.data;
  const max = Math.max(1, ...(o?.log.per_member.map((m) => m.count) ?? [1]));

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h2>Logovi</h2>
          <p>
            Šta se dešava na folderu, uživo iz <code>service.log</code>.
          </p>
        </div>
        <div className="spacer" />
        <LivePill status={status} paused={paused} />
        <button className="btn" onClick={() => setPaused(!paused)}>
          {paused ? <Play size={16} /> : <Pause size={16} />} {paused ? "Nastavi" : "Pauziraj"}
        </button>
      </div>

      <div className="stats">
        <div className="stat">
          <div className="k">Događaja danas</div>
          <div className="v">{o ? num(o.log.today) : "—"}</div>
          <div className="s">{o ? `+${num(o.log.last_hour)} u poslednjih sat` : ""}</div>
        </div>
        <div className="stat">
          <div className="k">Transferi (rsync)</div>
          <div className="v">{o ? num(o.log.transfers) : "—"}</div>
          <div className="s">{o ? `${num(o.log.uploads)} upload · ${num(o.log.downloads)} download` : ""}</div>
        </div>
        <div className="stat">
          <div className="k">Odbijeno</div>
          <div className="v" style={o && o.log.refused > 0 ? { color: "var(--g-err)" } : undefined}>
            {o ? num(o.log.refused) : "—"}
          </div>
          <div className="s">key refused / denied, danas</div>
        </div>
        {o && o.connected !== null ? (
          <div className="stat">
            <div className="k">Povezani klijenti</div>
            <div className="v">{num(o.connected)}</div>
            <div className="s">WebSocket veze</div>
          </div>
        ) : null}
      </div>

      <div className="split">
        <div>
          <div className="chips">
            {TABS.map(([t, label]) => (
              <button key={t} className={`chip ${tab === t ? "on" : ""}`} onClick={() => setTab(t)}>
                {label}
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
            <h4>Aktivnost po članu (1 h)</h4>
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
                <span className="side-note">Nema aktivnosti u poslednjih sat vremena.</span>
              )}
            </div>
          </div>
          <div className="card side-card">
            <h4>Server</h4>
            <div className="kv">
              <span className="k">Verzija</span>
              <span className="v mono">{o?.version ?? "—"}</span>
              <span className="k">Status</span>
              <span className="v">
                {o ? <span className={`badge ${o.server_ok ? "ok" : "err"}`}>{o.server_ok ? "ok" : "greška"}</span> : "—"}
              </span>
              <span className="k">Head seq</span>
              <span className="v">{o ? num(o.head) : "—"}</span>
              <span className="k">Zauzeće</span>
              <span className="v">{o ? `${bytes(o.files_size)} · ${num(o.files)} fajlova` : "—"}</span>
              <span className="k">Trash</span>
              <span className="v">{o ? `${bytes(o.trash_size)} · ${num(o.trash)}` : "—"}</span>
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}
