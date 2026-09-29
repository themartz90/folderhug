CREATE TABLE catalog_entries (
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL,
    id TEXT NOT NULL,
    parent_id TEXT NOT NULL,
    escaped_path TEXT NOT NULL,
    metadata TEXT NOT NULL,
    PRIMARY KEY (profile_id, generation, id)
);

CREATE INDEX catalog_entries_parent ON catalog_entries(profile_id, generation, parent_id);
