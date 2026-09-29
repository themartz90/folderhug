ALTER TABLE download_jobs ADD COLUMN destination_path TEXT NOT NULL DEFAULT '';
ALTER TABLE download_jobs ADD COLUMN mounted_name TEXT NOT NULL DEFAULT '';
