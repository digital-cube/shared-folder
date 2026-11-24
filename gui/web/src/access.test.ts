import { describe, expect, it } from "vitest";
import { allowedPages, autoFolder, defaultPage, pageFor, rememberedFolder, rememberFolder } from "./access";
import { kindLabel, passwordProblem, rulesHint } from "./pages/Account";

const admin = { kind: "account" as const, role: "admin", share_id: "s1" };
const member = { kind: "account" as const, role: "member", share_id: "s1" };
const noFolder = { kind: "account" as const, role: "", share_id: "" };
const token = { kind: "token" as const, role: "admin", share_id: "s1" };

describe("the menu by role", () => {
  it("an admin of the folder has every page", () => {
    expect(allowedPages(admin)).toEqual(["members", "logs", "files", "folders", "account", "install"]);
    expect(defaultPage(admin)).toBe("members");
  });

  it("a member who is not an admin has Files and Moj nalog, not Članovi nor Logovi", () => {
    expect(allowedPages(member)).toEqual(["files", "folders", "account", "install"]);
    expect(defaultPage(member)).toBe("files");
    // #members or #logs typed by hand opens Files instead.
    expect(pageFor(member, "members")).toBe("files");
    expect(pageFor(member, "logs")).toBe("files");
    expect(pageFor(member, "account")).toBe("account");
  });

  it("without a folder: the folder list and the account", () => {
    expect(allowedPages(noFolder)).toEqual(["folders", "account", "install"]);
    expect(pageFor(noFolder, "files")).toBe("folders");
  });

  it("a token session (one folder, admins only) has no account pages", () => {
    expect(allowedPages(token)).toEqual(["members", "logs", "files", "install"]);
    expect(pageFor(token, "account")).toBe("members");
  });
});

describe("the folder choice", () => {
  const F = [{ share_id: "a" }, { share_id: "b" }];

  it("opens the only folder at once, the remembered one if it is still there, else none", () => {
    expect(autoFolder([{ share_id: "a" }], null)).toBe("a");
    expect(autoFolder([{ share_id: "a" }], "zz")).toBe("a");
    expect(autoFolder(F, null)).toBeNull();
    expect(autoFolder(F, "b")).toBe("b");
    expect(autoFolder(F, "gone")).toBeNull();
    expect(autoFolder([], "a")).toBeNull();
  });

  it("is remembered per account", () => {
    const m = new Map<string, string>();
    const store = { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v) };
    rememberFolder("ana", "b", store);
    rememberFolder("bob", "a", store);
    expect(rememberedFolder("ana", store)).toBe("b");
    expect(rememberedFolder("bob", store)).toBe("a");
    expect(rememberedFolder("cica", store)).toBeNull();
    const broken = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("denied"); } };
    expect(rememberedFolder("ana", broken)).toBeNull();
    expect(() => rememberFolder("ana", "a", broken)).not.toThrow();
  });
});

describe("the password form", () => {
  const rules = { min_len: 12, max_len: 256, not_username: true, differ_from_old: true };
  it("checks the server's rules before sending", () => {
    expect(passwordProblem("ana", "stara-lozinka", "kratka", "kratka", rules)).toBe("Nova lozinka mora imati bar 12 znakova.");
    expect(passwordProblem("ana-marija-x", "stara", "Ana-Marija-X", "Ana-Marija-X", rules)).toMatch(/korisničko ime/);
    expect(passwordProblem("ana", "ista-lozinka-1", "ista-lozinka-1", "ista-lozinka-1", rules)).toMatch(/razlikuje od stare/);
    expect(passwordProblem("ana", "stara", "nova-dugacka-lozinka", "nova-dugacka-lozinkA", rules)).toBe("Ponovljena lozinka nije ista.");
    expect(passwordProblem("ana", "stara", "nova-dugacka-lozinka", "nova-dugacka-lozinka", rules)).toBeNull();
    // Characters, not bytes: 12 letters with diacritics are enough.
    expect(passwordProblem("ana", "stara", "čćžšđčćžšđčć", "čćžšđčćžšđčć", rules)).toBeNull();
  });

  it("says the rules and the kinds of devices", () => {
    expect(rulesHint(rules)).toBe("Najmanje 12 znakova, najviše 256; ne sme biti korisničko ime; različita od stare.");
    expect(kindLabel("gui")).toBe("GUI sesija");
    expect(kindLabel("client")).toBe("klijent (shf)");
  });
});
