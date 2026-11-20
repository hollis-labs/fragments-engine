-- +goose Up
-- Conversation files are kept short term, for evals: the eval process needs everything the
-- agent had access to until it has processed it, and they are purged afterwards. `kind` says
-- what the row is and `retention` says why it is kept, so the purge is one selector
-- (WHERE retention = 'purge_after_evals'). No purge job exists; this only marks them.
ALTER TABLE session_files
  ADD COLUMN kind      text NOT NULL DEFAULT 'brain_artifact',
  ADD COLUMN retention text NOT NULL DEFAULT 'purge_after_evals';
CREATE INDEX session_files_retention ON session_files (retention);

-- +goose Down
DROP INDEX session_files_retention;
ALTER TABLE session_files DROP COLUMN retention, DROP COLUMN kind;
