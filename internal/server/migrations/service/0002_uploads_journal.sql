-- Announced uploads waiting for their content and a commit.
CREATE TABLE uploads (
    id         TEXT PRIMARY KEY,           -- random hex; the file is tmp/<id>
    member_id  INTEGER NOT NULL REFERENCES members (id),
    path       TEXT NOT NULL,
    base_hash  TEXT NOT NULL DEFAULT '',
    new_hash   TEXT NOT NULL,
    size       INTEGER NOT NULL,
    mtime      INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL
);

-- Disk steps of an operation, written in the same transaction as its rows.
-- After a crash they are replayed on open; every step is idempotent.
CREATE TABLE fs_journal (
    id  INTEGER PRIMARY KEY,
    op  TEXT NOT NULL CHECK (op IN ('rename', 'mkdir', 'rmdir', 'remove')),
    src TEXT NOT NULL DEFAULT '',          -- relative to the share directory
    dst TEXT NOT NULL
);
