// The checks of the GUI's forms before they are sent: pure functions that
// say which field is wrong and why, in the UI's language. The submit button
// is never disabled because of an empty or wrong field: the click (or Enter)
// runs the check, and the message stands under that field (fieldProblem.tsx).
// The server checks again and has the last word. Rules: docs/gui.md,
// "Pravila za klik i forme".

import { renameTarget } from "../../shared/rename";
import { getLang, tr } from "./lang";

/** A wrong field of a form and the sentence about it; null when the form may go. */
export type FieldProblem<F extends string = string> = { field: F; text: string } | null;

/** A mild check of an email address (something@something.something); the server has the last word. */
export function looksLikeEmail(s: string): boolean {
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(s.trim());
}

/**
 * "Moj nalog → Email": the address against the saved one (`current`, as the
 * server keeps it: lower case, "" for none) and the account's password,
 * which confirms a change.
 */
export function emailProblem(current: string, value: string, pw: string): FieldProblem<"addr" | "pw"> {
  const v = value.trim();
  if (v.toLowerCase() === current) {
    return {
      field: "addr",
      text: current
        ? tr("Adresa je ista kao sačuvana. Upiši novu, pa sačuvaj.", "The address is the same as the saved one. Enter a new one, then save.")
        : tr("Upiši email adresu, pa sačuvaj.", "Enter an email address, then save."),
    };
  }
  if (v && !looksLikeEmail(v)) return { field: "addr", text: tr("To nije ispravna email adresa.", "That is not a valid email address.") };
  if (!pw) {
    return {
      field: "pw",
      text: !current
        ? tr("Za dodavanje email adrese upiši svoju lozinku.", "To add an email address, enter your password.")
        : !v
          ? tr("Za uklanjanje email adrese upiši svoju lozinku.", "To remove the email address, enter your password.")
          : tr("Za promenu email adrese upiši svoju lozinku.", "To change the email address, enter your password."),
    };
  }
  return null;
}

/** The sign-in page: the username and password, or the token. */
export function loginProblem(
  mode: "password" | "token",
  username: string,
  password: string,
  token: string,
): FieldProblem<"username" | "password" | "token"> {
  if (mode === "token") return token.trim() ? null : { field: "token", text: tr("Upiši token.", "Enter the token.") };
  if (!username.trim()) return { field: "username", text: tr("Upiši korisničko ime.", "Enter your username.") };
  if (!password) return { field: "password", text: tr("Upiši lozinku.", "Enter your password.") };
  return null;
}

/** "Zaboravljena lozinka": a username or an email (the server's sentence for an empty one). */
export function forgotProblem(login: string): FieldProblem<"login"> {
  return login.trim() ? null : { field: "login", text: tr("Upiši korisničko ime ili email.", "Enter your username or email.") };
}

/**
 * "Dodaj člana": the account to add. `picked` is the one clicked in the
 * results; otherwise the typed name must match exactly one of `found` (the
 * search for it), ignoring case.
 */
export function memberPick<U extends { username: string }>(q: string, found: readonly U[], picked: U | null): { user: U } | { field: "q"; text: string } {
  if (picked) return { user: picked };
  const term = q.trim();
  if (!term) return { field: "q", text: tr("Upiši korisničko ime naloga.", "Enter the username of the account.") };
  const exact = found.filter((u) => u.username.toLowerCase() === term.toLowerCase());
  if (exact.length === 1) return { user: exact[0] };
  if (found.length === 0) return { field: "q", text: tr("Nema naloga sa tim imenom.", "No account by that name.") };
  return { field: "q", text: tr("Izaberi nalog iz liste.", "Pick an account from the list.") };
}

/** "Dodaj člana → Napredno": the name of a member with a token. */
export function tokenMemberProblem(name: string): FieldProblem<"name"> {
  return name.trim() ? null : { field: "name", text: tr("Upiši ime člana.", "Enter the name of the member.") };
}

/** "Preimenuj": a name that will not do (renameTarget's sentence), or the same name as now. */
export function renameProblem(path: string, name: string): FieldProblem<"name"> {
  const target = renameTarget(path, name, getLang());
  if (typeof target !== "string") return { field: "name", text: target.error };
  if (target === path) return { field: "name", text: tr("Ime je isto kao sada. Upiši novo.", "The name is the same as now. Enter a new one.") };
  return null;
}
