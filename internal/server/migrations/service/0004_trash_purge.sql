-- A purged trash entry keeps its row (events refer to it through trash_id)
-- but loses its file; purged_at, purged_by and purge_seq say when, by whom
-- and under which purge event. Lists and verify skip purged rows.
ALTER TABLE trash ADD COLUMN purged_at TEXT;
ALTER TABLE trash ADD COLUMN purged_by TEXT NOT NULL DEFAULT '';
ALTER TABLE trash ADD COLUMN purge_seq INTEGER;
