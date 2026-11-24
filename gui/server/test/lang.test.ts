import { afterEach, describe, expect, it } from "vitest";
import { renameTarget } from "../../shared/rename.js";
import { buildGui, LOGIN_INVALID, LOGIN_INVALID_EN, LOGIN_NOT_ADMIN_EN, type GuiApp } from "../src/app.js";
import { toUdcError } from "../src/errors.js";
import { langOf } from "../src/lang.js";
import { errorMessage, ShfdError } from "../src/shfd.js";
import { FakeShfd } from "./fake-shfd.js";
import { fromPage } from "./origin.js";

let gui: GuiApp | undefined;
afterEach(async () => {
  await gui?.app.close();
  gui = undefined;
});

async function setup() {
  const fake = new FakeShfd();
  gui = await buildGui({ shfdUrl: "http://shfd", base: "/gui", secureCookie: true, fetchFn: fake.fetch, pollMs: 60_000 });
  return { fake, app: fromPage(gui.app) };
}

const login = (app: GuiApp["app"], token: string, lang?: string) =>
  app.inject({ method: "POST", url: "/gui/api/login", payload: { token }, headers: lang ? { "x-shfd-lang": lang } : {} });

describe("langOf", () => {
  it("is English only for exactly en, Serbian otherwise", () => {
    expect(langOf({ headers: { "x-shfd-lang": "en" } })).toBe("en");
    expect(langOf({ headers: { "x-shfd-lang": " EN " } })).toBe("en");
    expect(langOf({ headers: { "x-shfd-lang": "sr" } })).toBe("sr");
    expect(langOf({ headers: { "x-shfd-lang": "de" } })).toBe("sr");
    expect(langOf({ headers: {} })).toBe("sr");
  });
});

describe("errorMessage in English", () => {
  it("has an English sentence for every code, and Serbian by default", () => {
    expect(errorMessage("dest_exists")).toBe("Na toj putanji već postoji drugi fajl.");
    expect(errorMessage("dest_exists", undefined, "en")).toBe("Another file already exists at that path.");
    expect(errorMessage("invalid_request", "bad name", "en")).toBe("Invalid request: bad name");
    expect(errorMessage("whatever", "boom", "en")).toBe("Error: boom");
    expect(errorMessage("whatever", undefined, "en")).toBe("Something went wrong.");
    for (const code of ["member_exists", "last_admin", "unknown_member", "unauthorized", "forbidden", "unknown_trash", "trash_damaged", "not_found", "kind_mismatch", "invalid_path", "stale_base", "case_conflict", "is_dir", "dir_not_empty", "parent_not_dir", "file_changed", "unreachable"]) {
      expect(errorMessage(code, undefined, "en")).not.toBe(errorMessage(code, undefined, "sr"));
      expect(errorMessage(code, undefined, "en")).not.toMatch(/[čćžšđ]/i);
    }
  });

  it("toUdcError carries the language", () => {
    expect(toUdcError(new ShfdError(409, "stale_base", "x"), "en").message).toMatch(/changed in the meantime/);
    expect(toUdcError(new Error("x"), "en").message).toBe("Something went wrong.");
  });

  it("renameTarget says what is wrong in either language", () => {
    expect(renameTarget("a.txt", "")).toEqual({ error: "Ime ne može biti prazno." });
    expect(renameTarget("a.txt", "", "en")).toEqual({ error: "The name cannot be empty." });
    expect(renameTarget("a.txt", "x/y", "en")).toEqual({ error: "The name cannot contain / or \\." });
    expect(renameTarget("d/a.txt", "b.txt", "en")).toBe("d/b.txt");
  });
});

describe("the UI's language over HTTP", () => {
  it("sign-in refusals come back in English with x-shfd-lang: en", async () => {
    const { fake, app } = await setup();
    expect((await login(app, "shf_pogresan", "en")).json().error).toBe(LOGIN_INVALID_EN);
    expect((await login(app, "shf_pogresan")).json().error).toBe(LOGIN_INVALID);
    expect((await login(app, fake.members[1].token, "en")).json().error).toBe(LOGIN_NOT_ADMIN_EN);
  });

  it("action messages and refusals follow the header", async () => {
    const { fake, app } = await setup();
    const res = await login(app, fake.members[0].token);
    const cookie = String(res.headers["set-cookie"] ?? "").split(";")[0];
    const call = async (action: string, payload: object, lang: string) =>
      (await app.inject({ method: "POST", url: `/gui/udc/${action}`, payload, headers: { cookie, "x-shfd-lang": lang } })).json() as {
        kind: string;
        payload: any;
      };
    const taken = await call("shfd.file_rename", { path: "a.txt", name: "docs", base_hash: "aa".repeat(32) }, "en");
    expect(taken.payload.message).toBe("Another file already exists at that path.");
    const bad = await call("shfd.file_rename", { path: "a.txt", name: "x/y", base_hash: "aa".repeat(32) }, "en");
    expect(bad.payload.message).toBe("The name cannot contain / or \\.");
    const same = await call("shfd.file_rename", { path: "a.txt", name: "a.txt", base_hash: "aa".repeat(32) }, "en");
    expect(same.payload.message).toBe("The name is the same; nothing was changed.");
    const ok = await call("shfd.file_rename", { path: "docs/plan.md", name: "plan-v2.md", base_hash: "bb".repeat(32) }, "en");
    expect(ok.payload.message).toBe("Renamed to plan-v2.md.");
  });

  it("an expired session says so in English", async () => {
    const { app } = await setup();
    const r = await app.inject({ method: "POST", url: "/gui/udc/list", payload: { collection: "members" }, headers: { "x-shfd-lang": "en" } });
    expect(JSON.stringify(r.json())).toMatch(/The sign-in has expired\./);
  });
});
