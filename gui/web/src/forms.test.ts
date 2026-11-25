import { afterEach, describe, expect, it } from "vitest";
import { emailProblem, forgotProblem, loginProblem, looksLikeEmail, memberPick, renameProblem, tokenMemberProblem } from "./forms.js";
import { setLang } from "./lang.js";

afterEach(() => setLang("sr"));

describe("Moj nalog → Email", () => {
  it("says what is missing instead of a dead button", () => {
    expect(emailProblem("ana@example.com", "ana@example.com", "")).toEqual({
      field: "addr",
      text: "Adresa je ista kao sačuvana. Upiši novu, pa sačuvaj.",
    });
    // The same address in other case and with spaces is the same.
    expect(emailProblem("ana@example.com", "  Ana@Example.com ", "pw")?.field).toBe("addr");
    expect(emailProblem("", "", "")).toEqual({ field: "addr", text: "Upiši email adresu, pa sačuvaj." });
    expect(emailProblem("ana@example.com", "ana-at-example", "pw")).toEqual({ field: "addr", text: "To nije ispravna email adresa." });
    expect(emailProblem("", "ana@example.com", "")).toEqual({ field: "pw", text: "Za dodavanje email adrese upiši svoju lozinku." });
    expect(emailProblem("ana@example.com", "ana@example.org", "")).toEqual({ field: "pw", text: "Za promenu email adrese upiši svoju lozinku." });
    expect(emailProblem("ana@example.com", "", "")).toEqual({ field: "pw", text: "Za uklanjanje email adrese upiši svoju lozinku." });
    expect(emailProblem("ana@example.com", "ana@example.org", "pw")).toBeNull();
    expect(emailProblem("ana@example.com", "", "pw")).toBeNull();
  });

  it("speaks English too", () => {
    setLang("en");
    expect(emailProblem("ana@example.com", "ana@example.com", "")?.text).toBe("The address is the same as the saved one. Enter a new one, then save.");
    expect(emailProblem("a@b.c", "nope", "")?.text).toBe("That is not a valid email address.");
    expect(emailProblem("", "ana@example.com", "")?.text).toBe("To add an email address, enter your password.");
    expect(emailProblem("a@b.c", "ana@example.com", "")?.text).toBe("To change the email address, enter your password.");
    expect(emailProblem("a@b.c", "", "")?.text).toBe("To remove the email address, enter your password.");
  });

  it("checks an address mildly; the server has the last word", () => {
    expect(looksLikeEmail("a@b.c")).toBe(true);
    expect(looksLikeEmail("ana.marija+x@mail.example.rs")).toBe(true);
    expect(looksLikeEmail("ana@example")).toBe(false);
    expect(looksLikeEmail("ana example@x.rs")).toBe(false);
    expect(looksLikeEmail("@x.rs")).toBe(false);
  });
});

describe("sign-in and the forgotten password", () => {
  it("names the empty field", () => {
    expect(loginProblem("password", "", "", "")).toEqual({ field: "username", text: "Upiši korisničko ime." });
    expect(loginProblem("password", "  ", "pw", "")?.field).toBe("username");
    expect(loginProblem("password", "ana", "", "")).toEqual({ field: "password", text: "Upiši lozinku." });
    expect(loginProblem("password", "ana", "pw", "")).toBeNull();
    expect(loginProblem("token", "", "", " ")).toEqual({ field: "token", text: "Upiši token." });
    expect(loginProblem("token", "", "", "shf_x")).toBeNull();
    expect(forgotProblem(" ")).toEqual({ field: "login", text: "Upiši korisničko ime ili email." });
    expect(forgotProblem("ana")).toBeNull();
    setLang("en");
    expect(loginProblem("password", "ana", "", "")?.text).toBe("Enter your password.");
    expect(forgotProblem("")?.text).toBe("Enter your username or email.");
  });
});

describe("Dodaj člana", () => {
  const ana = { username: "ana", display_name: "Ana" };
  const anaM = { username: "ana-marija", display_name: "" };

  it("adds the clicked account, or the one the typed name matches exactly", () => {
    expect(memberPick("", [], anaM)).toEqual({ user: anaM });
    expect(memberPick("ANA", [ana, anaM], null)).toEqual({ user: ana });
    expect(memberPick(" ana-marija ", [ana, anaM], null)).toEqual({ user: anaM });
  });

  it("says what is wrong otherwise, never a silent close", () => {
    expect(memberPick("  ", [], null)).toEqual({ field: "q", text: "Upiši korisničko ime naloga." });
    expect(memberPick("an", [ana, anaM], null)).toEqual({ field: "q", text: "Izaberi nalog iz liste." });
    expect(memberPick("zoran", [], null)).toEqual({ field: "q", text: "Nema naloga sa tim imenom." });
    expect(tokenMemberProblem(" ")).toEqual({ field: "name", text: "Upiši ime člana." });
    expect(tokenMemberProblem("marko-laptop")).toBeNull();
    setLang("en");
    expect(memberPick("an", [ana, anaM], null)).toEqual({ field: "q", text: "Pick an account from the list." });
  });
});

describe("Preimenuj", () => {
  it("shows the empty name's message and refuses the same name", () => {
    expect(renameProblem("docs/a.txt", "")).toEqual({ field: "name", text: "Ime ne može biti prazno." });
    expect(renameProblem("docs/a.txt", " a.txt ")).toEqual({ field: "name", text: "Ime je isto kao sada. Upiši novo." });
    expect(renameProblem("docs/a.txt", "a/b")?.field).toBe("name");
    expect(renameProblem("docs/a.txt", "b.txt")).toBeNull();
    setLang("en");
    expect(renameProblem("a.txt", "a.txt")?.text).toBe("The name is the same as now. Enter a new one.");
    expect(renameProblem("a.txt", "")?.text).toBe("The name cannot be empty.");
  });
});
