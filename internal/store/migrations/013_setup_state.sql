-- Existing installations must never become claimable if their owner row is lost.
INSERT OR IGNORE INTO settings(key,value)
SELECT 'owner_setup_complete','1'
WHERE EXISTS (SELECT 1 FROM owner_auth)
   OR EXISTS (SELECT 1 FROM settings)
   OR EXISTS (SELECT 1 FROM profiles)
   OR EXISTS (SELECT 1 FROM auth_sessions)
   OR EXISTS (SELECT 1 FROM api_tokens)
   OR EXISTS (SELECT 1 FROM download_jobs)
   OR EXISTS (SELECT 1 FROM scan_jobs)
   OR EXISTS (SELECT 1 FROM owner_preferences);
