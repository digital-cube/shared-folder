-- Size and mtime (Unix nanoseconds, as sent by the uploading client) of the
-- content a put, conflict or move event leaves at its path, so a client
-- that downloads it can give the file the same mtime.
ALTER TABLE events ADD COLUMN size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE events ADD COLUMN mtime INTEGER NOT NULL DEFAULT 0;
