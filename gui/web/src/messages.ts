import type { Messages } from "@jsudc/react";

// Column titles and texts of the collections, in Serbian (Latin). The
// English entry is the same text: the GUI has one language.
const sr = (s: string) => ({ sr: s, en: s });

export const messages: Messages = {
  APP: {
    TAB_TABLE: sr("Tabela"),
    TAB_THUMBS: sr("Mreža"),
  },
  COLLECTIONS: {
    MEMBERS: sr("Članovi"),
    LOGS: sr("Logovi"),
    FILES: sr("Fajlovi"),
    TRASH: sr("Trash"),
  },
  TBL_MEMBERS: {
    USERNAME: sr("Član"),
    ROLE: sr("Uloga"),
    STATUS: sr("Status"),
    LAST_SEEN_AT: sr("Poslednji put viđen"),
    LAST_PUSH_AT: sr("Poslednji upis"),
    BEHIND: sr("Kasni"),
    SSH_FINGERPRINT: sr("SSH ključ"),
    SEARCH_PLACEHOLDER: sr("Traži člana"),
    EMPTY_PLAIN: sr("Nema članova."),
  },
  TBL_LOGS: {
    TS: sr("Vreme"),
    KIND: sr("Tip"),
    MEMBER: sr("Član"),
    OP: sr("Operacija"),
    PATH: sr("Putanja"),
    SEQ: sr("seq"),
    SEARCH_PLACEHOLDER: sr("Putanja, član, op…"),
    EMPTY_PLAIN: sr("Log je prazan."),
  },
  TBL_FILES: {
    NAME: sr("Ime"),
    SIZE: sr("Veličina"),
    MTIME: sr("Izmenjeno"),
    UPDATED_BY: sr("Izmenio"),
    HASH: sr("Hash"),
    EXT: sr("Ekstenzija"),
    SEARCH_PLACEHOLDER: sr("Traži u folderu"),
    EMPTY_PLAIN: sr("Folder je prazan."),
  },
  TBL_TRASH: {
    ORIG_PATH: sr("Putanja"),
    REASON: sr("Razlog"),
    BY: sr("Ko"),
    AT: sr("Kad"),
    SIZE: sr("Veličina"),
    SEARCH_PLACEHOLDER: sr("Putanja ili član"),
    EMPTY_PLAIN: sr("Trash je prazan."),
  },
};
