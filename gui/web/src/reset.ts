// The pages of a forgotten password, shown without a sign-in: #forgot (the
// form that asks for a link) and #reset?token=… (where the mailed link
// leads). Pure helpers; the pages are in pages/Reset.tsx.

import type { FieldProblem } from "./forms";
import { tr } from "./lang";

export type AuthPage = "forgot" | "reset";

/** The page of a hash when it is one of the two: "#forgot", "#reset?token=…". */
export function authPage(hash: string): AuthPage | null {
  const h = hash.replace(/^#\/?/, "").split(/[/?]/)[0];
  return h === "forgot" || h === "reset" ? h : null;
}

/** The token of "#reset?token=…"; "" when there is none. */
export function resetToken(hash: string): string {
  const q = hash.indexOf("?");
  if (q < 0) return "";
  return (new URLSearchParams(hash.slice(q + 1)).get("token") ?? "").trim();
}

/** The smallest length of a password (password.MinLen on shfd); the server checks again. */
export const MIN_PASSWORD = 12;

/**
 * What is wrong with a new password of the reset page before it is sent,
 * and in which field; null when nothing is. The page does not know the
 * username, so that rule (and the rest) is the server's to check.
 */
export function resetProblem(pw: string, repeat: string, min = MIN_PASSWORD): FieldProblem<"pw" | "repeat"> {
  if (!pw) return { field: "pw", text: tr("Upiši novu lozinku.", "Enter the new password.") };
  if ([...pw].length < min) return { field: "pw", text: tr(`Nova lozinka mora imati bar ${min} znakova.`, `The new password must have at least ${min} characters.`) };
  if (!repeat) return { field: "repeat", text: tr("Ponovi novu lozinku.", "Repeat the new password.") };
  if (pw !== repeat) return { field: "repeat", text: tr("Ponovljena lozinka nije ista.", "The repeated password does not match.") };
  return null;
}

/** The note on the sign-in page after a reset. */
export function resetDoneNote(username: string): string {
  return tr(`Lozinka naloga ${username} je promenjena. Prijavi se novom lozinkom.`, `The password of ${username} was changed. Sign in with the new password.`);
}
