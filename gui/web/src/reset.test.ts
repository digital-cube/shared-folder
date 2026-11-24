import { afterEach, describe, expect, it } from "vitest";
import { forgotPassword, resetPassword, setMyEmail } from "./api.js";
import { setLang } from "./lang.js";
import { authPage, resetDoneNote, resetProblem, resetToken } from "./reset.js";

describe("the pages of a forgotten password", () => {
  afterEach(() => setLang("sr"));

  it("reads #forgot and #reset?token=… from the hash", () => {
    expect(authPage("#forgot")).toBe("forgot");
    expect(authPage("#/forgot")).toBe("forgot");
    expect(authPage("#reset?token=abc")).toBe("reset");
    expect(authPage("#reset")).toBe("reset");
    expect(authPage("#files")).toBeNull();
    expect(authPage("")).toBeNull();
    expect(resetToken("#reset?token=Ab-_9xyz")).toBe("Ab-_9xyz");
    expect(resetToken("#reset?x=1&token=%20t1%20")).toBe("t1");
    expect(resetToken("#reset")).toBe("");
    expect(resetToken("#reset?token=")).toBe("");
  });

  it("checks the new password twice before it goes out", () => {
    expect(resetProblem("kratka", "kratka")).toBe("Nova lozinka mora imati bar 12 znakova.");
    expect(resetProblem("dovoljno-duga-1", "dovoljno-duga-2")).toBe("Ponovljena lozinka nije ista.");
    expect(resetProblem("dovoljno-duga-1", "dovoljno-duga-1")).toBeNull();
    // Characters, not bytes: 12 letters with diacritics are enough.
    expect(resetProblem("čćžšđčćžšđčć", "čćžšđčćžšđčć")).toBeNull();
    setLang("en");
    expect(resetProblem("short", "short")).toBe("The new password must have at least 12 characters.");
    expect(resetDoneNote("ana")).toBe("The password of ana was changed. Sign in with the new password.");
  });
});

describe("the calls of the pages", () => {
  const calls: { url: string; method: string; body: unknown; lang: string }[] = [];
  const fake = (status: number, body: unknown) =>
    (async (url: string, init: RequestInit) => {
      calls.push({ url, method: String(init.method), body: JSON.parse(String(init.body)), lang: (init.headers as Record<string, string>)["x-shfd-lang"] });
      return new Response(JSON.stringify(body), { status });
    }) as unknown as typeof fetch;

  it("forgot, reset and the email go to the GUI server's routes", async () => {
    calls.length = 0;
    const r1 = await forgotPassword("ana", fake(202, { ok: true, message: "poslato" }));
    expect(r1).toEqual({ ok: true, data: { ok: true, message: "poslato" } });
    const r2 = await resetPassword("tok", "nova-lozinka-12", fake(400, { error: "Link nije ispravan", code: "invalid_reset_token" }));
    expect(r2).toEqual({ ok: false, status: 400, error: "Link nije ispravan", code: "invalid_reset_token" });
    await setMyEmail("ana@example.com", "pw", fake(200, { ok: true, email: "ana@example.com" }));
    expect(calls.map((c) => [c.method, c.url, c.body])).toEqual([
      ["POST", "/api/auth/forgot", { login: "ana" }],
      ["POST", "/api/auth/reset", { token: "tok", new_password: "nova-lozinka-12" }],
      ["PUT", "/api/me/email", { email: "ana@example.com", current_password: "pw" }],
    ]);
    expect(calls.every((c) => c.lang === "sr")).toBe(true);
  });
});
