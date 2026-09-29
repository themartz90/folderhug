CREATE TABLE download_jobs (
    id TEXT PRIMARY KEY,
    profile_id TEXT REFERENCES profiles(id) ON DELETE SET NULL,
    generation INTEGER NOT NULL,
    creator_id TEXT NOT NULL,
    lease_id TEXT NOT NULL,
    owner INTEGER NOT NULL,
    entry_id TEXT NOT NULL,
    escaped_path TEXT NOT NULL,
    name TEXT NOT NULL,
    destination_id TEXT NOT NULL,
    requested_connections INTEGER NOT NULL,
    connections INTEGER NOT NULL DEFAULT 1,
    bps_limit INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL,
    transferred INTEGER NOT NULL DEFAULT 0,
    total_bytes INTEGER,
    etag TEXT NOT NULL DEFAULT '',
    range_verified INTEGER NOT NULL DEFAULT 0,
    restart_required INTEGER NOT NULL DEFAULT 0,
    fallback_reason TEXT,
    file_available INTEGER NOT NULL DEFAULT 0,
    publishing INTEGER NOT NULL DEFAULT 0,
    checksum TEXT NOT NULL DEFAULT '',
    error TEXT,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX download_job_order ON download_jobs(created_at DESC,id DESC);
CREATE INDEX download_queue ON download_jobs(state,created_at,id);
CREATE TABLE download_segments (
    job_id TEXT NOT NULL REFERENCES download_jobs(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    start_byte INTEGER NOT NULL,
    end_byte INTEGER NOT NULL,
    durable_bytes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(job_id,number)
);
CREATE TABLE download_idempotency (
    creator_id TEXT NOT NULL,
    key TEXT NOT NULL,
    payload_hash TEXT NOT NULL,
    job_id TEXT NOT NULL REFERENCES download_jobs(id),
    expires_at INTEGER NOT NULL,
    PRIMARY KEY(creator_id,key)
);
