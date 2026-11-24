-- A forgotten password (docs/admin.md, "Zaboravljena lozinka"): an account
-- gets an email address, and a reset link mailed to it carries a one-time
-- token of which only the SHA-256 is kept here. Times are RFC 3339 in UTC.

-- Lowercase; NULL when the account has none. Unique among those that have
-- one, so "username or email" names one account.
ALTER TABLE users ADD COLUMN email TEXT;
CREATE UNIQUE INDEX users_email ON users (email) WHERE email IS NOT NULL;

CREATE TABLE password_resets (
    id         INTEGER PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id),
    token_hash TEXT NOT NULL UNIQUE,                 -- hex SHA-256 of the token
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,                        -- created_at + 30 min
    used_at    TEXT,                                 -- used, or made void by a newer request or a reset
    ip         TEXT NOT NULL DEFAULT ''              -- who asked; "cli" for shfd user reset-link
);
CREATE INDEX password_resets_user ON password_resets (user_id, created_at);
