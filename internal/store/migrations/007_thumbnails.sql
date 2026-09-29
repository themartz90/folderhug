CREATE TABLE thumbnails (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL,
    entry_id TEXT NOT NULL,
    cache_key TEXT NOT NULL UNIQUE,
    state TEXT NOT NULL CHECK(state IN ('pending','ready','unavailable','disabled')),
    file_name TEXT NOT NULL DEFAULT '',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    validator TEXT NOT NULL DEFAULT '',
    validated_at INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL,
    accessed_at INTEGER NOT NULL
);
CREATE INDEX thumbnails_expiry ON thumbnails(expires_at, accessed_at);
CREATE INDEX thumbnails_profile ON thumbnails(profile_id);
