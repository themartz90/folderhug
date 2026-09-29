CREATE TABLE scan_jobs (
    id TEXT PRIMARY KEY,
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL,
    creator_id TEXT NOT NULL,
    lease_id TEXT NOT NULL,
    owner INTEGER NOT NULL,
    origin TEXT NOT NULL,
    directory_id TEXT NOT NULL,
    root_path TEXT NOT NULL,
    input TEXT NOT NULL,
    state TEXT NOT NULL,
    directories_visited INTEGER NOT NULL DEFAULT 0,
    files_found INTEGER NOT NULL DEFAULT 0,
    directories_failed INTEGER NOT NULL DEFAULT 0,
    directories_skipped INTEGER NOT NULL DEFAULT 0,
    entries_seen INTEGER NOT NULL DEFAULT 0,
    elapsed_seconds REAL NOT NULL DEFAULT 0,
    partial INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    error TEXT
);
CREATE UNIQUE INDEX scan_active_profile ON scan_jobs(profile_id)
    WHERE state IN ('queued','running','paused','waiting_for_credentials','interrupted');
CREATE INDEX scan_job_order ON scan_jobs(created_at DESC,id DESC);
CREATE TABLE scan_frontier (
    scan_id TEXT NOT NULL REFERENCES scan_jobs(id) ON DELETE CASCADE,
    path_key TEXT NOT NULL,
    escaped_path TEXT NOT NULL,
    depth INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    error_code TEXT,
    PRIMARY KEY(scan_id,path_key)
);
CREATE INDEX scan_frontier_pending ON scan_frontier(scan_id,state,depth,path_key);
CREATE TABLE scan_results (
    scan_id TEXT NOT NULL REFERENCES scan_jobs(id) ON DELETE CASCADE,
    entry_id TEXT NOT NULL,
    metadata TEXT NOT NULL,
    PRIMARY KEY(scan_id,entry_id)
);
CREATE TABLE scan_index (
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL,
    entry_id TEXT NOT NULL,
    escaped_path TEXT NOT NULL,
    name_fold TEXT NOT NULL,
    path_fold TEXT NOT NULL,
    natural_key TEXT NOT NULL COLLATE BINARY,
    category TEXT NOT NULL,
    extension TEXT,
    size INTEGER,
    modified_at TEXT,
    metadata TEXT NOT NULL,
    last_scan_id TEXT NOT NULL,
    PRIMARY KEY(profile_id,generation,entry_id)
);
CREATE INDEX scan_index_order ON scan_index(natural_key,profile_id,entry_id);
CREATE TABLE scan_index_revision (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL);
INSERT INTO scan_index_revision VALUES(1,0);
CREATE TABLE scan_idempotency (
    creator_id TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    key TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    response TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY(creator_id,profile_id,key)
);
