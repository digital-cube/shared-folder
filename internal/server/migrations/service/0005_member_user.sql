-- A member may be linked to an account of registry.db (users.id); NULL is
-- a member that has only its token. An account is at most one member of a
-- folder.
ALTER TABLE members ADD COLUMN user_id INTEGER;
CREATE UNIQUE INDEX members_user ON members (user_id) WHERE user_id IS NOT NULL;
