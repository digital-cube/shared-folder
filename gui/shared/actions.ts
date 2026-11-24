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
