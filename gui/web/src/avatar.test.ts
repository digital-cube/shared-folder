// The Avatar component, the picture form of Moj nalog and their helpers
// (TCK33XX9NH). Rendered to static markup: no DOM is needed for what they
// show at first, and the helpers carry the logic.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createElement as h } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { AVATAR_ACCEPT, AVATAR_MAX_BYTES, avatarColor, avatarFileProblem, avatarHue, avatarSrc, initials } from "../../shared/avatar";
import { removeAvatar, uploadAvatar } from "./api";
import { SessionContext, type SessionApi } from "./App";
import { Avatar } from "./Avatar";
import { LangProvider } from "./i18n";
import { setLang } from "./lang";
import { AvatarForm, avatarProblemText } from "./pages/Account";

describe("initials", () => {
  it("come from the display name, else the parts of the username", () => {
    expect(initials("ana", "Ana Anić")).toBe("AA");
    expect(initials("igor", "igor ilić petrović")).toBe("II");
    expect(initials("ana.petrovic")).toBe("AP");
    expect(initials("marko_m")).toBe("MM");
    expect(initials("bob", "")).toBe("B");
    expect(initials("bob", "  ")).toBe("B");
    expect(initials("šaban")).toBe("Š");
    expect(initials("x", "Đorđe")).toBe("Đ");
    expect(initials("", "")).toBe("?");
  });
});

describe("the colour of the initials", () => {
  it("is the same for the same username, in any letter case, and differs between names", () => {
    expect(avatarColor("ana")).toBe(avatarColor("ANA"));
    expect(avatarColor("ana")).toMatch(/^hsl\(\d{1,3} 55% 40%\)$/);
    const hues = new Set(["ana", "bob", "cid", "marija", "igor", "marko"].map(avatarHue));
    expect(hues.size).toBeGreaterThan(3);
    for (const u of ["a", "zz", "ana.petrovic"]) expect(avatarHue(u)).toBeLessThan(360);
  });
});

describe("avatarSrc", () => {
  it("turns shfd's avatar_url into the GUI's, under its base", () => {
    expect(avatarSrc("/v1/users/ana/avatar?v=1732500000000", "/gui")).toBe("/gui/api/avatar/ana?v=1732500000000");
    expect(avatarSrc("/v1/users/ana/avatar?v=3", "")).toBe("/api/avatar/ana?v=3");
    expect(avatarSrc(null, "/gui")).toBeNull();
    expect(avatarSrc(undefined, "/gui")).toBeNull();
    expect(avatarSrc("https://evil.example/x.jpg", "/gui")).toBeNull();
    expect(avatarSrc("/v1/users/ana/avatar", "/gui")).toBeNull();
  });
});

describe("a chosen file", () => {
  it("is checked for its type and size before it is sent", () => {
    expect(avatarFileProblem({ size: 1000, type: "image/png" })).toBeNull();
    expect(avatarFileProblem({ size: 1000, type: "image/WEBP" })).toBeNull();
    expect(avatarFileProblem({ size: AVATAR_MAX_BYTES, type: "image/gif" })).toBeNull();
    expect(avatarFileProblem({ size: AVATAR_MAX_BYTES + 1, type: "image/jpeg" })).toBe("too_large");
    expect(avatarFileProblem({ size: 10, type: "image/svg+xml" })).toBe("unsupported");
    expect(avatarFileProblem({ size: 10, type: "text/html" })).toBe("unsupported");
    // No type (an unknown extension): shfd decides from the content.
    expect(avatarFileProblem({ size: 10, type: "" })).toBeNull();
    expect(AVATAR_ACCEPT).toBe("image/png,image/jpeg,image/webp,image/gif");
  });

  it("problems are sentences in both languages", () => {
    setLang("sr");
    expect(avatarProblemText("too_large")).toBe("Slika je prevelika; najviše 5 MB.");
    expect(avatarProblemText("unsupported")).toContain("nije slika u podržanom formatu");
    setLang("en");
    expect(avatarProblemText("too_large")).toBe("The picture is too large; 5 MB at most.");
    expect(avatarProblemText("unsupported")).toContain("not a picture in a supported format");
    setLang("sr");
  });
});

describe("<Avatar>", () => {
  it("shows the picture when there is one", () => {
    const html = renderToStaticMarkup(h(Avatar, { username: "ana", url: "/v1/users/ana/avatar?v=5", base: "/gui", size: 48 }));
    expect(html).toContain('src="/gui/api/avatar/ana?v=5"');
    expect(html).toContain("has-img");
    expect(html).toContain("width:48px");
    expect(html).not.toContain(">A<");
  });

  it("shows the initials on the account's colour without one", () => {
    const html = renderToStaticMarkup(h(Avatar, { username: "ana", displayName: "Ana Anić", url: null }));
    expect(html).toContain(">AA</span>");
    expect(html).toContain(`background:${avatarColor("ana")}`);
    expect(html).not.toContain("<img");
  });

  it("a preview wins over the saved picture; a revoked member is grey", () => {
    expect(renderToStaticMarkup(h(Avatar, { username: "ana", url: "/v1/users/ana/avatar?v=5", preview: "blob:x" }))).toContain('src="blob:x"');
    expect(renderToStaticMarkup(h(Avatar, { username: "ana", muted: true }))).toContain("var(--g-faint)");
  });
});

function renderForm(current: string | null) {
  const session: SessionApi = {
    me: { kind: "account", username: "ana", display_name: "Ana Anić", must_change: false, share_id: "", share_name: "", role: "", member: "ana", avatar_url: current },
    setMe: () => {},
    openFolder: async () => null,
    signOut: () => {},
    signedOut: () => {},
  };
  return renderToStaticMarkup(
    h(
      LangProvider,
      null,
      h(QueryClientProvider, { client: new QueryClient() }, h(SessionContext.Provider, { value: session }, h(AvatarForm, { username: "ana", displayName: "Ana Anić", current }))),
    ),
  );
}

describe("the picture form of Moj nalog", () => {
  it("without a picture: the initials, a file input for the four types, no Ukloni", () => {
    setLang("sr");
    const html = renderForm(null);
    expect(html).toContain(">AA</span>");
    expect(html).toContain(`accept="${AVATAR_ACCEPT}"`);
    expect(html).toContain("Izaberi sliku…");
    expect(html).not.toContain('data-testid="avatar-remove"');
    expect(html).not.toContain('data-testid="avatar-save"');
    expect(html).toContain("najviše 5 MB");
  });

  it("with a picture: the picture, Izaberi drugu and Ukloni", () => {
    setLang("en");
    const html = renderForm("/v1/users/ana/avatar?v=9");
    expect(html).toContain("/api/avatar/ana?v=9");
    expect(html).toContain("Choose another picture…");
    expect(html).toContain('data-testid="avatar-remove"');
    expect(html).toContain(">Remove</button>");
    setLang("sr");
  });
});

describe("the calls", () => {
  it("upload sends the file as it is with its type; remove is a DELETE", async () => {
    const seen: { url: string; method?: string; type?: string | null; body?: unknown }[] = [];
    const f = (async (url: string, init?: RequestInit) => {
      seen.push({ url, method: init?.method, type: new Headers(init?.headers).get("content-type"), body: init?.body });
      return new Response(JSON.stringify({ ok: true, avatar_url: null, session: {} }), { status: 200 });
    }) as typeof fetch;
    const file = new Blob([new Uint8Array([0x89, 0x50])], { type: "image/png" });
    expect((await uploadAvatar(file, f)).ok).toBe(true);
    expect(seen[0]).toMatchObject({ method: "PUT", type: "image/png", body: file });
    expect(seen[0].url).toMatch(/\/api\/me\/avatar$/);
    await uploadAvatar(new Blob([new Uint8Array([1])]), f);
    expect(seen[1].type).toBe("application/octet-stream");
    await removeAvatar(f);
    expect(seen[2]).toMatchObject({ method: "DELETE" });
  });

  it("a refusal comes back as its sentence and code", async () => {
    const f = (async () => new Response(JSON.stringify({ error: "Slika je prevelika; najviše 5 MB.", code: "image_too_large" }), { status: 413 })) as unknown as typeof fetch;
    const r = await uploadAvatar(new Blob([new Uint8Array([1])], { type: "image/png" }), f);
    expect(r).toEqual({ ok: false, status: 413, error: "Slika je prevelika; najviše 5 MB.", code: "image_too_large" });
  });
});
