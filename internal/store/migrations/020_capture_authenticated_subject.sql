-- Historic principal_id values are caller assertions, not proof of mesh identity.
-- Only new authenticated mesh captures receive this server-stamped binding.
ALTER TABLE capture_attempts ADD COLUMN authenticated_subject TEXT NOT NULL DEFAULT '';
