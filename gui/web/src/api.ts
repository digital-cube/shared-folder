// The GUI's own endpoints (sign-in); everything else goes through jsudc.

export interface Me {
  share_id: string;
  share_name: string;
  username: string;
  role: string;
}

export const BASE = "/gui";

export async function getSession(): Promise<Me | null> {
  const r = await fetch(`${BASE}/api/session`, { credentials: "same-origin" });
  if (r.status === 401) return null;
  if (!r.ok) throw new Error(`session: ${r.status}`);
  return r.json();
}

export async function login(token: string): Promise<{ ok: true; me: Me } | { ok: false; error: string }> {
  let r: Response;
  try {
    r = await fetch(`${BASE}/api/login`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ token }),
    });
  } catch {
    return { ok: false, error: "GUI server ne odgovara." };
  }
  const body = await r.json().catch(() => ({}));
  if (!r.ok) return { ok: false, error: body.error ?? "Prijava nije uspela." };
  return { ok: true, me: body };
}

export async function logout(): Promise<void> {
  await fetch(`${BASE}/api/logout`, { method: "POST", credentials: "same-origin" });
}
