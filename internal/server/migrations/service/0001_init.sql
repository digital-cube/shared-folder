-- Schema of one folder (docs/specifikacija.md, section 3). Times are RFC 3339
-- in UTC; members are referred to by username everywhere except uploads.

CREATE TABLE members (
    id             INTEGER PRIMARY KEY,
    username       TEXT NOT NULL UNIQUE,
    token_hash     TEXT NOT NULL UNIQUE,   -- hex SHA-256 of the token
    ssh_pubkey     TEXT NOT NULL DEFAULT '',
    role           TEXT NOT NULL CHECK (role IN ('admin', 'member')),
    created_at     TEXT NOT NULL,
    revoked_at     TEXT,
    last_seen_at   TEXT,
    last_push_at   TEXT,
    last_seq_acked INTEGER NOT NULL DEFAULT 0
);

-- Current state. Directories are rows too, so empty ones exist.
CREATE TABLE files (
    path       TEXT PRIMARY KEY,
    fold_key   TEXT NOT NULL UNIQUE,       -- pathx.FoldKey(path)
    kind       TEXT NOT NULL CHECK (kind IN ('file', 'dir')),
    hash       TEXT NOT NULL DEFAULT '',
    size       INTEGER NOT NULL DEFAULT 0,
    mtime      INTEGER NOT NULL DEFAULT 0, -- Unix nanoseconds, as sent by the client
    seq        INTEGER NOT NULL,           -- event of the last change
    updated_by TEXT NOT NULL
);

CREATE TABLE trash (
    id         INTEGER PRIMARY KEY,
    orig_path  TEXT NOT NULL,
    trash_name TEXT NOT NULL UNIQUE,       -- file name inside trash/
    fold_key   TEXT NOT NULL UNIQUE,       -- keeps trash/ safe on case-insensitive disks
    hash       TEXT NOT NULL,
    size       INTEGER NOT NULL,
    reason     TEXT NOT NULL CHECK (reason IN ('deleted', 'replaced')),
    by_member  TEXT NOT NULL,
    at         TEXT NOT NULL,
    event_seq  INTEGER NOT NULL
);

-- The change log; seq has no gaps and the largest one is HEAD.
CREATE TABLE events (
    seq       INTEGER PRIMARY KEY,
    ts        TEXT NOT NULL,
    member    TEXT NOT NULL,
    op        TEXT NOT NULL CHECK (op IN ('put', 'delete', 'move', 'mkdir', 'rmdir',
                                          'conflict', 'restore', 'purge')),
    path      TEXT NOT NULL,
    old_path  TEXT NOT NULL DEFAULT '',
    hash      TEXT NOT NULL DEFAULT '',
    prev_hash TEXT NOT NULL DEFAULT '',
    trash_id  INTEGER REFERENCES trash (id)
);

CREATE TABLE sync_log (
    id          INTEGER PRIMARY KEY,
    member      TEXT NOT NULL,
    started_at  TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    from_seq    INTEGER NOT NULL,
    to_seq      INTEGER NOT NULL,
    sent        INTEGER NOT NULL,
    received    INTEGER NOT NULL,
    conflicts   INTEGER NOT NULL,
    error       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sync_log_member ON sync_log (member, id);
