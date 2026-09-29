CREATE TABLE owner_auth (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT NOT NULL
);

CREATE TABLE auth_sessions (
    id TEXT PRIMARY KEY,
    secret_hash BLOB NOT NULL UNIQUE,
    csrf_token TEXT NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE TABLE api_tokens (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    secret_hash BLOB NOT NULL UNIQUE,
    profile_ids TEXT NOT NULL,
    scopes TEXT NOT NULL,
    expires_at INTEGER,
    created_at INTEGER NOT NULL
);
