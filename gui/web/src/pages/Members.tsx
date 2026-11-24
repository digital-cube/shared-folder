import type { FilterNode, Row } from "@jsudc/core";
import { UdcDataView, useUdcClient, type BulkActionContext } from "@jsudc/react";
import { useToast } from "@jsudc/react/ui";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import {
  addAccountMember,
  addMember,
  memberDevices,
  reactivateMember,
  resetToken,
  revokeMember,
  revokeMemberDevice,
  searchUsers,
  setRole,
  type MemberDeviceRow,
} from "../../../shared/actions";
import { useApp } from "../App";
import { AddMember, Confirm, Modal } from "../dialogs";
import { ago, joinCommand, stamp } from "../format";
import { useTr } from "../i18n";
import { tr as trNow } from "../lang";
import { AlertTriangle, Plus } from "../icons";
import { kindLabel } from "./Account";

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
  const [devicesOf, setDevicesOf] = useState<string | null>(null);
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
    devices: ({ row }: BulkActionContext) => setDevicesOf(nameOf(row)),
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
        // A member of an account comes back without a token: its devices sign in.
        if (r.token) setRevealed({ username: r.username, token: r.token, what: tr("Član je vraćen. Nov token za", "Member restored. New token for") });
        else toast({ title: tr(`${r.username} je vraćen.`, `${r.username} was restored.`), variant: "success" });
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
          <h2>{tr("Članovi", "Members")}</h2>
          <p>
            {tr(
              "Član je nalog; svaki njegov uređaj ima svoje kredencijale i opoziva se posebno. Članovi „bez naloga“ rade starim tokenom (jedan član = jedan uređaj).",
              "A member is an account; each of its devices has its own credentials and is revoked on its own. Members “without an account” work with an old token (one member = one device).",
            )}
          </p>
        </div>
        <div className="spacer" />
        <button className="btn primary" onClick={() => setAdding(true)}>
          <Plus size={16} /> {tr("Dodaj člana", "Add a member")}
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
        <AddMember
          onClose={() => setAdding(false)}
          search={async (q) => (await client.call(searchUsers, { q })).users}
          onAddAccount={async (user, admin) => {
            try {
              const r = await client.call(addAccountMember, { user, admin });
              toast({
                title: r.reactivated
                  ? tr(`${r.username} je ponovo član.`, `${r.username} is a member again.`)
                  : tr(`${r.username} je dodat u folder.`, `${r.username} was added to the folder.`),
                variant: "success",
              });
              refresh();
              return null;
            } catch (err) {
              return messageOf(err);
            }
          }}
          onAddToken={async (username, admin) => {
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

      {devicesOf ? <MemberDevices username={devicesOf} onClose={() => setDevicesOf(null)} onChanged={refresh} /> : null}

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

/** The devices of a member of an account in this folder, each with its own credentials, and their revoke. */
function MemberDevices({ username, onClose, onChanged }: { username: string; onClose: () => void; onChanged: () => void }) {
  const tr = useTr();
  const client = useUdcClient();
  const [data, setData] = useState<{ account: string; devices: MemberDeviceRow[] } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [asking, setAsking] = useState<MemberDeviceRow | null>(null);

  useEffect(() => {
    client.call(memberDevices, { username }).then(setData, (err) => setError(messageOf(err)));
  }, [client, username]);

  const revoke = async (d: MemberDeviceRow) => {
    setAsking(null);
    try {
      setData(await client.call(revokeMemberDevice, { username, device: d.id }));
      onChanged();
    } catch (err) {
      setError(messageOf(err));
    }
  };

  return (
    <Modal title={tr(`Uređaji: ${username}`, `Devices: ${username}`)} onClose={onClose} wide>
      <p>
        {tr("Nalog", "Account")} <b>{data?.account ?? "…"}</b>.{" "}
        {tr(
          "Opoziv gasi kredencijale tog uređaja u ovom folderu (API i rsync); ostali uređaji rade dalje.",
          "Revoking ends that device's credentials in this folder (API and rsync); the other devices keep working.",
        )}
      </p>
      {error ? (
        <div className="err-text" role="alert">
          {error}
        </div>
      ) : null}
      <table className="plain" data-testid="member-devices">
        <thead>
          <tr>
            <th>{tr("Ime", "Name")}</th>
            <th>{tr("Tip", "Type")}</th>
            <th>{tr("Otisak", "Fingerprint")}</th>
            <th>{tr("Poslednji put viđen", "Last seen")}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {(data?.devices ?? []).map((d) => (
            <tr key={d.id} className={d.revoked ? "revoked" : ""}>
              <td>
                <b>{d.name}</b>
                {d.connected > 0 ? <span className="badge ok">{tr("povezan", "connected")}</span> : null}
              </td>
              <td>
                <span className={`badge ${d.kind === "gui" ? "info" : ""}`}>{kindLabel(d.kind)}</span>
              </td>
              <td className="mono muted small" title={d.ssh_fingerprint}>
                {d.ssh_fingerprint ? `${d.ssh_fingerprint.slice(0, 19)}…` : <span className="faint">{tr("nema ključa", "no key")}</span>}
              </td>
              <td title={stamp(d.last_seen_at)}>{ago(d.last_seen_at)}</td>
              <td className="right">
                {d.revoked ? (
                  <span className="badge err">{tr("opozvan", "revoked")}</span>
                ) : (
                  <button className="btn sm danger" onClick={() => setAsking(d)} data-testid={`revoke-member-device-${d.id}`}>
                    {tr("Opozovi", "Revoke")}
                  </button>
                )}
              </td>
            </tr>
          ))}
          {data && data.devices.length === 0 ? (
            <tr>
              <td colSpan={5} className="muted">
                {tr("Nijedan uređaj se još nije priključio ovom folderu.", "No device has joined this folder yet.")}
              </td>
            </tr>
          ) : null}
        </tbody>
      </table>
      <div className="row">
        <button className="btn" onClick={onClose}>
          {tr("Zatvori", "Close")}
        </button>
      </div>
      {asking ? (
        <Confirm
          title={tr(`Opozovi uređaj ${asking.name}?`, `Revoke the device ${asking.name}?`)}
          text={tr(
            "Uređaj odmah gubi pristup ovom folderu. Da bi se vratio, korisnik ga ponovo priključuje (shf join).",
            "The device loses access to this folder at once. To come back, the user joins it again (shf join).",
          )}
          confirm={tr("Opozovi", "Revoke")}
          danger
          onClose={() => setAsking(null)}
          onYes={() => void revoke(asking)}
        />
      ) : null}
    </Modal>
  );
}
