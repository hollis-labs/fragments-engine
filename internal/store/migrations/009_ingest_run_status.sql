-- Async ingest runs: track lifecycle status and failure detail.
-- Existing rows are completed synchronous runs, so they default to 'done'.
ALTER TABLE ingest_runs ADD COLUMN status TEXT NOT NULL DEFAULT 'done';
ALTER TABLE ingest_runs ADD COLUMN error TEXT NOT NULL DEFAULT '';
