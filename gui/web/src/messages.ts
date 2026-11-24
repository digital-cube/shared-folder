import type { Messages } from "@jsudc/react";

// Column titles and texts of the collections, in Serbian (Latin) and English.
const t = (sr: string, en: string) => ({ sr, en });

export const messages: Messages = {
  APP: {
    TAB_TABLE: t("Tabela", "Table"),
    TAB_THUMBS: t("Mreža", "Grid"),
  },
  COLLECTIONS: {
    MEMBERS: t("Članovi", "Members"),
    LOGS: t("Logovi", "Logs"),
    FILES: t("Fajlovi", "Files"),
    TRASH: t("Trash", "Trash"),
  },
  TBL_MEMBERS: {
    USERNAME: t("Član", "Member"),
    ACCOUNT: t("Nalog", "Account"),
    ROLE: t("Uloga", "Role"),
    STATUS: t("Status", "Status"),
    LAST_SEEN_AT: t("Poslednji put viđen", "Last seen"),
    LAST_PUSH_AT: t("Poslednji upis", "Last push"),
    BEHIND: t("Kasni", "Behind"),
    DEVICES: t("Uređaji", "Devices"),
    SSH_FINGERPRINT: t("SSH ključ (token)", "SSH key (token)"),
    SEARCH_PLACEHOLDER: t("Traži člana ili nalog", "Search members or accounts"),
    EMPTY_PLAIN: t("Nema članova.", "No members."),
    // the row actions of the server's schema (collections.ts)
    ACTION_DEVICES: t("Uređaji", "Devices"),
    ACTION_RESET_TOKEN: t("Nov token", "New token"),
    ACTION_MAKE_ADMIN: t("Učini adminom", "Make admin"),
    ACTION_MAKE_MEMBER: t("Oduzmi admina", "Remove admin"),
    ACTION_REVOKE: t("Opozovi", "Revoke"),
    ACTION_REACTIVATE: t("Vrati člana", "Reinstate member"),
  },
  TBL_LOGS: {
    TS: t("Vreme", "Time"),
    KIND: t("Tip", "Type"),
    MEMBER: t("Član", "Member"),
    OP: t("Operacija", "Operation"),
    PATH: t("Putanja", "Path"),
    SEQ: t("seq", "seq"),
    SEARCH_PLACEHOLDER: t("Putanja, član, op…", "Path, member, op…"),
    EMPTY_PLAIN: t("Log je prazan.", "The log is empty."),
  },
  TBL_FILES: {
    NAME: t("Ime", "Name"),
    SIZE: t("Veličina", "Size"),
    MTIME: t("Izmenjeno", "Modified"),
    UPDATED_BY: t("Izmenio", "Modified by"),
    HASH: t("Hash", "Hash"),
    EXT: t("Ekstenzija", "Extension"),
    SEARCH_PLACEHOLDER: t("Traži u folderu", "Search this folder"),
    EMPTY_PLAIN: t("Folder je prazan.", "The folder is empty."),
  },
  TBL_TRASH: {
    ORIG_PATH: t("Putanja", "Path"),
    REASON: t("Razlog", "Reason"),
    BY: t("Ko", "By"),
    AT: t("Kad", "When"),
    SIZE: t("Veličina", "Size"),
    SEARCH_PLACEHOLDER: t("Putanja ili član", "Path or member"),
    EMPTY_PLAIN: t("Trash je prazan.", "The trash is empty."),
    ACTION_RESTORE: t("Vrati", "Restore"),
  },
};
