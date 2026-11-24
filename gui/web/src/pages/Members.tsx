import type { FilterNode, Row } from "@jsudc/core";
import { UdcDataView, useUdcClient, type BulkActionContext } from "@jsudc/react";
import { useToast } from "@jsudc/react/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { addMember, reactivateMember, resetToken, revokeMember, setRole } from "../../../shared/actions";
import { useApp } from "../App";
import { Confirm, NewMember } from "../dialogs";
import { joinCommand } from "../format";
import { useTr } from "../i18n";
import { tr as trNow } from "../lang";
import { AlertTriangle, Plus } from "../icons";

type Tab = "all" | "active" | "revoked" | "admins";
const TABS: [Tab, string, string][] = [
  ["all", "Svi", "All"],
  ["active", "Aktivni", "Active"],
  ["revoked", "Opozvani", "Revoked"],
  ["admins", "Admini", "Admins"],
];

const FILTERS: Record<Tab, FilterNode | undefined> = {
  all: undefined,
  active: { field: "status", op: "neq", value: "revoked" },
  revoked: { field: "status", op: "eq", value: "revoked" },
  admins: { and: [{ field: "role", op: "eq", value: "admin" }, { field: "status", op: "neq", value: "revoked" }] },
};

interface Revealed {
  username: string;
  token: string;
  what: string;
}

/** The sentence of a failed action (the server already wrote it in the UI's language). */
export function messageOf(err: unknown): string {
  const m = (err as { message?: string })?.message;
  return m && !/^[a-z_]+$/.test(m) ? m : trNow("Nešto nije uspelo.", "Something went wrong.");
}

export function Members() {
  const { me, signOut } = useApp();
  const client = useUdcClient();
  const qc = useQueryClient();
  const { toast } = useToast();
  const tr = useTr();
  const [tab, setTab] = useState<Tab>("all");
  const [revealed, setRevealed] = useState<Revealed | null>(null);
  const [adding, setAdding] = useState(false);
  const [confirm, setConfirm] = useState<null | { row: Row; kind: "revoke" | "make_member" }>(null);
  const [copied, setCopied] = useState<string | null>(null);

  // Counts of the filter chips.
  const counts = useQuery({
    queryKey: ["member-counts"],
    queryFn: async () => {
      const r = await client.list({ collection: "members", page: { limit: 500 } });
      const rows = r.rows as Row[];
      return {
        all: rows.length,
        active: rows.filter((m) => m.status !== "revoked").length,
        revoked: rows.filter((m) => m.status === "revoked").length,
        admins: rows.filter((m) => m.role === "admin" && m.status !== "revoked").length,
      };
    },
    refetchInterval: 15_000,
  });

  const refresh = () => {
    void qc.invalidateQueries();
  };
  const fail = (err: unknown) => toast({ title: messageOf(err), variant: "danger" });

  const run = async (f: () => Promise<void>) => {
    try {
      await f();
    } catch (err) {
      fail(err);
    } finally {
      refresh();
    }
  };

  const nameOf = (row?: Row) => String(row?.username ?? "");

  const actions = {
    reset_token: ({ row }: BulkActionContext) =>
      run(async () => {
        const r = await client.call(resetToken, { username: nameOf(row) });
        if (r.username === me.username) {
          toast({ title: tr("Tvoj token je zamenjen; prijavi se novim.", "Your token was replaced; sign in with the new one."), variant: "warning" });
        }
        setRevealed({ username: r.username, token: r.token, what: tr("Nov token za", "New token for") });
      }),
    make_admin: ({ row }: BulkActionContext) =>
      run(async () => {
        await client.call(setRole, { username: nameOf(row), role: "admin" });
        toast({ title: tr(`${nameOf(row)} je sada admin.`, `${nameOf(row)} is now an admin.`), variant: "success" });
      }),
    make_member: ({ row }: BulkActionContext) => setConfirm({ row: row!, kind: "make_member" }),
    revoke: ({ row }: BulkActionContext) => setConfirm({ row: row!, kind: "revoke" }),
    reactivate: ({ row }: BulkActionContext) =>
      run(async () => {
        const r = await client.call(reactivateMember, { username: nameOf(row) });
        setRevealed({ username: r.username, token: r.token, what: tr("Član je vraćen. Nov token za", "Member restored. New token for") });
      }),
  };

  const copy = async (text: string, what: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(what);
      setTimeout(() => setCopied(null), 1500);
    } catch {
      toast({ title: tr("Kopiranje nije uspelo; označi tekst i kopiraj ga ručno.", "Copying failed; select the text and copy it by hand."), variant: "warning" });
    }
  };

  const join = revealed ? joinCommand(location.origin, me.share_id, revealed.token, me.share_name) : "";

  return (
    <div className="page">
      <div className="page-head">
        <div>
          <h2>{tr("Članovi i tokeni", "Members and tokens")}</h2>
          <p>{tr("Jedan član = jedan uređaj. Token se vidi samo kad se napravi.", "One member = one device. A token is shown only when it is created.")}</p>
        </div>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setAdding(true)}>
          <Plus size={16} /> {tr("Nov član", "New member")}
        </button>
      </div>

      {revealed ? (
        <div className="reveal" role="status">
          <span className="ic">
            <AlertTriangle size={18} />
          </span>
          <div className="grow">
            <b>
              {revealed.what} <code>{revealed.username}</code>
            </b>
            <span className="muted">{tr("Sačuvaj ga sada, čuva se samo hash i neće se ponovo prikazati.", "Save it now: only its hash is kept and it will not be shown again.")}</span>
            <div className="tok">
              <code data-testid="new-token">{revealed.token}</code>
              <button className="btn sm" onClick={() => copy(revealed.token, "token")}>
                {copied === "token" ? tr("Kopirano", "Copied") : tr("Kopiraj", "Copy")}
              </button>
              <button className="btn sm" onClick={() => copy(join, "join")} title={join}>
                {copied === "join" ? tr("Kopirano", "Copied") : tr("Kopiraj shf join komandu", "Copy the shf join command")}
              </button>
            </div>
          </div>
          <button className="btn ghost sm" onClick={() => setRevealed(null)}>
            {tr("Zatvori", "Close")}
          </button>
        </div>
      ) : null}

      <div className="chips">
        {TABS.map(([t, sr, en]) => (
          <button key={t} className={`chip ${tab === t ? "on" : ""}`} onClick={() => setTab(t)}>
            {tr(sr, en)} <span className="n">{counts.data ? counts.data[t] : ""}</span>
          </button>
        ))}
        <span className="spacer" style={{ flex: 1 }} />
        <span className="side-note">
          {tr("Kolona", "The")} <b>{tr("Kasni", "Behind")}</b>{" "}
          {tr("= koliko događaja član još nije potvrdio", "column = how many events the member has not confirmed yet")}
        </span>
      </div>

      <div className="view-card">
        <UdcDataView collection="members" filter={FILTERS[tab]} urlState="memory" actions={actions} chrome="embedded" />
      </div>

      {adding ? (
        <NewMember
          onClose={() => setAdding(false)}
          onAdd={async (username, admin) => {
            try {
              const r = await client.call(addMember, { username, admin });
              setRevealed({
                username: r.username,
                token: r.token,
                what: r.reactivated
                  ? tr("Opozvan član je vraćen. Nov token za", "Revoked member restored. New token for")
                  : tr("Nov token za", "New token for"),
              });
              refresh();
              return null;
            } catch (err) {
              return messageOf(err);
            }
          }}
        />
      ) : null}

      {confirm ? (
        <Confirm
          title={
            confirm.kind === "revoke"
              ? tr(`Opozovi ${nameOf(confirm.row)}?`, `Revoke ${nameOf(confirm.row)}?`)
              : tr(`Oduzmi ulogu admin članu ${nameOf(confirm.row)}?`, `Take the admin role from ${nameOf(confirm.row)}?`)
          }
          text={
            confirm.kind === "revoke"
              ? tr(
                  "Token odmah prestaje da važi, a uređaj se odspaja. Član ostaje u listi i istoriji i može se vratiti.",
                  "The token stops working at once and the device is disconnected. The member stays in the list and the history and can be restored.",
                )
              : nameOf(confirm.row) === me.username
                ? tr("To si ti: posle ovoga više nećeš moći da koristiš GUI.", "That is you: after this you will no longer be able to use the GUI.")
                : tr("Član ostaje aktivan, ali bez admin prava.", "The member stays active, but without admin rights.")
          }
          confirm={confirm.kind === "revoke" ? tr("Opozovi", "Revoke") : tr("Oduzmi", "Take away")}
          danger
          onClose={() => setConfirm(null)}
          onYes={() =>
            run(async () => {
              const username = nameOf(confirm.row);
              if (confirm.kind === "revoke") {
                await client.call(revokeMember, { username });
                toast({ title: tr(`${username} je opozvan.`, `${username} was revoked.`), variant: "success" });
              } else {
                await client.call(setRole, { username, role: "member" });
                toast({ title: tr(`${username} više nije admin.`, `${username} is no longer an admin.`), variant: "success" });
              }
              if (username === me.username) signOut();
            })
          }
        />
      ) : null}
    </div>
  );
}
