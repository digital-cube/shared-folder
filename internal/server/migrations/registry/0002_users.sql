-- Accounts that sign in with a password (docs/admin.md, "Nalozi"). A
-- member of a folder may be linked to one of them (members.user_id in the
-- folder's service.db); user_shares is the index of those links, so
-- "my folders" needs no scan of every folder. Times are RFC 3339 in UTC.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,          -- pathx.ValidateUsername, lowercase
    display_name  TEXT NOT NULL DEFAULT '',
    pw_hash       TEXT NOT NULL,                 -- PHC string of argon2id
    must_change   INTEGER NOT NULL DEFAULT 0,    -- temporary password: change it on first sign-in
    created_at    TEXT NOT NULL,
    disabled_at   TEXT,
    last_login_at TEXT
);

-- One row per signed-in device or GUI session.
CREATE TABLE devices (
    id           INTEGER PRIMARY KEY,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL CHECK (kind IN ('client','gui')),
    token_hash   TEXT NOT NULL UNIQUE,           -- hex SHA-256 of the device token
    created_at   TEXT NOT NULL,
    last_seen_at TEXT,
    expires_at   TEXT,                           -- gui: sliding 12 h, at most 7 d; client: NULL
    revoked_at   TEXT
);
CREATE INDEX devices_user ON devices (user_id);

-- Index of members.user_id over all folders; the folders' databases are
-- the source of truth and shfd verify repairs this table from them.
CREATE TABLE user_shares (
    user_id   INTEGER NOT NULL REFERENCES users(id),
    share_id  TEXT NOT NULL REFERENCES shares(id),
    member_id INTEGER NOT NULL,
    PRIMARY KEY (user_id, share_id)
);

-- Every sign-in attempt: the audit trail, and what the attempt limit counts
-- (so it survives a restart). at has a fixed width so that it sorts.
CREATE TABLE auth_log (
    at       TEXT NOT NULL,
    username TEXT NOT NULL,
    ip       TEXT NOT NULL,
    outcome  TEXT NOT NULL                        -- ok | bad_password | unknown_user | locked | disabled
);
CREATE INDEX auth_log_username ON auth_log (username, at);
CREATE INDEX auth_log_ip ON auth_log (ip, at);
