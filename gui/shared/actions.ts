// Host actions of shfd-gui, imported by the server and the browser. Each
// one is a call of shfd's admin API with the signed-in member's token.

import { defineHostAction } from "@jsudc/core";
import { z } from "zod";

const member = z.object({ username: z.string().min(1) });

/** A new member, or a revoked one brought back. The token is shown once. */
export const addMember = defineHostAction({
  name: "shfd.member_add",
  request: z.object({ username: z.string().min(1).max(64), admin: z.boolean() }),
  response: z.object({ username: z.string(), token: z.string(), reactivated: z.boolean() }),
});

export const revokeMember = defineHostAction({
  name: "shfd.member_revoke",
  request: member,
  response: z.object({ username: z.string(), status: z.string() }),
});

export const setRole = defineHostAction({
  name: "shfd.member_set_role",
  request: z.object({ username: z.string().min(1), role: z.enum(["admin", "member"]) }),
  response: z.object({ username: z.string(), role: z.string(), old_role: z.string() }),
});

export const resetToken = defineHostAction({
  name: "shfd.member_reset_token",
  request: member,
  response: z.object({ username: z.string(), token: z.string() }),
});

/** A revoked member brought back with its old role (shfd member add). */
export const reactivateMember = defineHostAction({
  name: "shfd.member_reactivate",
  request: member,
  response: z.object({ username: z.string(), token: z.string(), reactivated: z.boolean() }),
});

export const restoreTrash = defineHostAction({
  name: "shfd.trash_restore",
  request: z.object({ id: z.string().min(1) }),
  response: z.object({ path: z.string(), status: z.string(), renamed: z.boolean(), message: z.string() }),
});

/** What the header, the sidebar and the log tiles show. */
export const overview = defineHostAction({
  name: "shfd.overview",
  request: z.object({}),
  response: z.object({
    /** A member who is not an admin gets the server's state only; the numbers are 0. */
    limited: z.boolean(),
    version: z.string(),
    server_ok: z.boolean(),
    head: z.number(),
    files: z.number(),
    files_size: z.number(),
    trash: z.number(),
    trash_size: z.number(),
    members: z.number(),
    connected: z.number().nullable(),
    log: z.object({
      today: z.number(),
      last_hour: z.number(),
      transfers: z.number(),
      uploads: z.number(),
      downloads: z.number(),
      refused: z.number(),
      per_member: z.array(z.object({ member: z.string(), count: z.number() })),
    }),
  }),
});

/**
 * A file given a new name in its folder: a move of shfd's sync protocol
 * (POST /ops, op move), so every member's device renames it too. base_hash
 * is the version the GUI showed; a file changed since is not renamed.
 */
export const renameFile = defineHostAction({
  name: "shfd.file_rename",
  request: z.object({ path: z.string().min(1), name: z.string().min(1).max(255), base_hash: z.string() }),
  response: z.object({ path: z.string(), status: z.string(), message: z.string() }),
});

/**
 * A file deleted (op delete: it goes to the trash, every device deletes it)
 * or an empty folder removed (op rmdir).
 */
export const removeFile = defineHostAction({
  name: "shfd.file_remove",
  request: z.object({ path: z.string().min(1), kind: z.enum(["file", "dir"]), base_hash: z.string() }),
  response: z.object({ path: z.string(), status: z.string(), message: z.string() }),
});

/** An account added to the folder as a member (admin/members {user, admin}); no token. */
export const addAccountMember = defineHostAction({
  name: "shfd.member_add_account",
  request: z.object({ user: z.string().min(1).max(64), admin: z.boolean() }),
  response: z.object({ username: z.string(), account: z.string(), reactivated: z.boolean() }),
});

/** Accounts whose username starts with q (or whose display name has it), for "Dodaj člana". */
export const searchUsers = defineHostAction({
  name: "shfd.users_search",
  request: z.object({ q: z.string().max(64) }),
  response: z.object({ users: z.array(z.object({ username: z.string(), display_name: z.string() })) }),
});

const memberDevice = z.object({
  id: z.number(),
  name: z.string(),
  kind: z.string(),
  ssh_fingerprint: z.string(),
  created_at: z.string(),
  last_seen_at: z.string().nullable(),
  revoked: z.boolean(),
  connected: z.number(),
});
export type MemberDeviceRow = z.infer<typeof memberDevice>;

/** The devices of a member in this folder: an account's devices, each with its own credentials. */
export const memberDevices = defineHostAction({
  name: "shfd.member_devices",
  request: member,
  response: z.object({ username: z.string(), account: z.string(), devices: z.array(memberDevice) }),
});

/** Revokes one device's credentials in this folder; the member's other devices keep working. */
export const revokeMemberDevice = defineHostAction({
  name: "shfd.member_device_revoke",
  request: z.object({ username: z.string().min(1), device: z.number().int().positive() }),
  response: z.object({ username: z.string(), account: z.string(), devices: z.array(memberDevice) }),
});
