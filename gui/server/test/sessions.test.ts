import { describe, expect, it } from "vitest";
import { COOKIE, IDLE_MS, readCookie, sessionCookie, SessionStore } from "../src/sessions.js";

const base = { token: "shf_x", shareId: "s1", shareName: "projekti", username: "ana", role: "admin" };

describe("SessionStore", () => {
  it("creates random ids and finds the session", () => {
    const st = new SessionStore();
    const a = st.create(base);
    const b = st.create(base);
    expect(a.id).not.toBe(b.id);
    expect(a.id.length).toBeGreaterThanOrEqual(40);
    expect(st.get(a.id)?.username).toBe("ana");
    expect(st.get("nema")).toBeUndefined();
    expect(st.get(undefined)).toBeUndefined();
  });

  it("expires after 12 h without use, and use keeps it alive", () => {
    let now = 0;
    const st = new SessionStore(() => now);
    const s = st.create(base);
    now = IDLE_MS - 1;
    expect(st.get(s.id)).toBeDefined(); // refreshed here
    now += IDLE_MS - 1;
    expect(st.get(s.id)).toBeDefined();
    now += IDLE_MS + 1;
    expect(st.get(s.id)).toBeUndefined();
    expect(st.size()).toBe(0);
  });

  it("drops every session of a token and lists folders", () => {
    const st = new SessionStore();
    st.create(base);
    st.create(base);
    const other = st.create({ ...base, token: "shf_y", shareId: "s2" });
    expect(st.shares().sort()).toEqual(["s1", "s2"]);
    st.dropToken("shf_x");
    expect(st.forShare("s1")).toEqual([]);
    expect(st.get(other.id)).toBeDefined();
    st.delete(other.id);
    expect(st.size()).toBe(0);
  });
});

describe("cookies", () => {
  it("reads one cookie among several", () => {
    expect(readCookie(`a=1; ${COOKIE}=abc%20d; b=2`, COOKIE)).toBe("abc d");
    expect(readCookie("a=1", COOKIE)).toBeUndefined();
    expect(readCookie(undefined, COOKIE)).toBeUndefined();
  });

  it("is httpOnly, SameSite=Strict and Secure, never carries the token", () => {
    const c = sessionCookie("id1", { path: "/gui/", secure: true });
    expect(c).toBe(`${COOKIE}=id1; Path=/gui/; HttpOnly; SameSite=Strict; Secure`);
    expect(sessionCookie("", { path: "/gui/", secure: false, maxAgeSec: 0 })).toContain("Max-Age=0");
  });
});
