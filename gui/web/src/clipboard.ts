// Copying to the clipboard, the one way every page does it (docs/gui.md,
// "Pravila za klik i forme"): copyText says whether it worked, and the page
// shows the outcome either way. The clipboard is missing on a page opened
// over plain http, and the browser may refuse it.

import { detectOs, type Os } from "./os";

type Clip = { writeText(text: string): Promise<void> } | undefined;

/** Puts text on the clipboard; false when there is none or it refused. */
export async function copyText(text: string, clip: Clip = typeof navigator === "undefined" ? undefined : navigator.clipboard): Promise<boolean> {
  if (!clip?.writeText) return false;
  try {
    await clip.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/** The keys that copy on this system: "⌘C" on macOS, "Ctrl+C" elsewhere. */
export function copyKeys(os: Os = detectOs()): string {
  return os === "macos" ? "⌘C" : "Ctrl+C";
}

/** Selects the text of an element, for the person to copy by hand. */
export function selectText(el: Node | null): void {
  const sel = typeof window === "undefined" ? null : window.getSelection();
  if (!el || !sel) return;
  const range = document.createRange();
  range.selectNodeContents(el);
  sel.removeAllRanges();
  sel.addRange(range);
}
