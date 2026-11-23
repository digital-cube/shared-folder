-- Folders served from this data-root.
CREATE TABLE shares (
    id         TEXT PRIMARY KEY,          -- UUID, also the directory name
    name       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL              -- RFC 3339, UTC
);
