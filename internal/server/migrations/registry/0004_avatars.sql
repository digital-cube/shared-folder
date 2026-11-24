-- A profile picture of an account (docs/admin.md, "Profilna slika"). The
-- picture itself is <db-root>/avatars/<users.id>.jpg (avatars.go); the
-- registry keeps only whether there is one and which. avatar_version is
-- NULL when the account has no picture and changes with every new one,
-- so the picture's URL (…/avatar?v=<version>) may be cached for good.

ALTER TABLE users ADD COLUMN avatar_version INTEGER;
ALTER TABLE users ADD COLUMN avatar_updated_at TEXT;
