-- +goose Up
-- Policy flags on tool-result parts. Text and JSON parts are redacted before they
-- are stored; binary parts (images) cannot be, so they are marked not_redactable.
-- read_policy is what a reader enforces; every part is 'open' today. Setting a
-- part to 'withheld' later needs no schema change and no re-ingest.
ALTER TABLE tool_results
  ADD COLUMN redaction_state text NOT NULL DEFAULT 'redacted' CHECK (redaction_state IN ('redacted','not_redactable')),
  ADD COLUMN read_policy     text NOT NULL DEFAULT 'open'     CHECK (read_policy IN ('open','withheld'));

-- Binary payloads are never redacted, so they must say so.
UPDATE tool_results SET redaction_state = 'not_redactable' WHERE bytes_redacted IS NOT NULL;
ALTER TABLE tool_results
  ADD CONSTRAINT binary_is_not_redactable CHECK (bytes_redacted IS NULL OR redaction_state = 'not_redactable');
CREATE INDEX tool_results_policy ON tool_results (read_policy) WHERE read_policy <> 'open';

-- +goose Down
DROP INDEX tool_results_policy;
ALTER TABLE tool_results DROP CONSTRAINT binary_is_not_redactable, DROP COLUMN read_policy, DROP COLUMN redaction_state;
