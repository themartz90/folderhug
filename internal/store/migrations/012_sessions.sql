ALTER TABLE auth_sessions ADD COLUMN remembered INTEGER NOT NULL DEFAULT 0 CHECK (remembered IN (0,1));
ALTER TABLE auth_sessions ADD COLUMN absolute_expires_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE auth_sessions ADD COLUMN last_rotated_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE auth_sessions ADD COLUMN previous_secret_hash BLOB;
ALTER TABLE auth_sessions ADD COLUMN previous_expires_at INTEGER NOT NULL DEFAULT 0;

UPDATE auth_sessions SET absolute_expires_at = expires_at;
CREATE INDEX auth_sessions_previous_secret ON auth_sessions(previous_secret_hash);
