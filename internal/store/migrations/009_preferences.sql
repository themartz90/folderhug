CREATE TABLE owner_preferences (
    id INTEGER PRIMARY KEY CHECK(id=1),
    settings TEXT NOT NULL
);

CREATE TABLE bookmarks (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL,
    directory_id TEXT NOT NULL,
    name TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0 CHECK(position>=0)
);
CREATE INDEX bookmarks_order ON bookmarks(position,id);
